package gatewayclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/funclaw/go-worker/internal/protocol"
	ws "github.com/gorilla/websocket"
)

const (
	operatorReadScope  = "operator.read"
	operatorWriteScope = "operator.write"
	gatewayDialTimeout = 10 * time.Second
)

// GatewayClient is a client for calling OpenClaw Gateway.
//
// 这里的关键点：
// 1. responses.create 现在走 Gateway 的 WS `agent`，不是旧的 /v1/responses。
// 2. Gateway WS 会夹杂 health/tick 等 event，所以必须按 req id 等待真正的 res。
// 3. responses.create 的输入要转换成 agent 所需的 message + attachments。
type GatewayClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	wsURL      string
}

// New creates a new Gateway client.
func New(baseURL, token, wsURL string) *GatewayClient {
	return &GatewayClient{
		baseURL: baseURL,
		token:   token,
		wsURL:   wsURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CallAgent calls the agent method via WebSocket on Gateway.
func (c *GatewayClient) CallAgent(ctx context.Context, input interface{}, sessionKey string) (interface{}, error) {
	fmt.Printf("[Gateway] CallAgent called with sessionKey=%s\n", sessionKey)

	inputJSON, _ := json.MarshalIndent(input, "", "  ")
	fmt.Printf("[Gateway] CallAgent input: %s\n", string(inputJSON))

	baselineSeq, err := c.getLatestHistorySeq(ctx, sessionKey)
	if err != nil {
		// 历史基线拿不到不阻断主流程，后面再回退。
		fmt.Printf("[Gateway] CallAgent baseline history lookup failed, continue without baseline: %v\n", err)
		baselineSeq = 0
	}

	gw, err := c.connectOperatorSocket(ctx)
	if err != nil {
		return nil, err
	}
	defer gw.Close()

	agentParams := transformToAgentParams(input, sessionKey)
	reqPayload := map[string]interface{}{
		"type":   "req",
		"id":     "agent-1",
		"method": "agent",
		"params": agentParams,
	}

	reqJSON, _ := json.MarshalIndent(reqPayload, "", "  ")
	fmt.Printf("[Gateway] CallAgent sending: %s\n", string(reqJSON))

	if err := gw.WriteJSON(reqPayload); err != nil {
		return nil, fmt.Errorf("failed to send agent request: %w", err)
	}

	initialResp, err := c.readMatchingResponse(gw, "agent-1")
	if err != nil {
		return nil, err
	}
	payload, ok := extractPayloadMap(initialResp)
	if !ok {
		return nil, fmt.Errorf("missing payload in agent response: %v", initialResp)
	}

	runID, ok := payload["runId"].(string)
	if !ok {
		return nil, fmt.Errorf("missing runId in agent response: %v", payload)
	}
	status, _ := payload["status"].(string)
	fmt.Printf("[Gateway] CallAgent accepted, runId=%s, status=%s\n", runID, status)

	waitPayload, err := c.waitForAgentCompletion(gw, runID)
	if err != nil {
		return nil, err
	}

	// agent.wait 只给状态，不给最终文本；真正结果从 session history 里补回。
	if sessionKey != "" {
		historyResult, historyErr := c.buildAgentResultFromHistory(ctx, sessionKey, baselineSeq)
		if historyErr == nil {
			fmt.Printf("[Gateway] CallAgent history-derived result extracted: %v\n", historyResult)
			return historyResult, nil
		}
		fmt.Printf("[Gateway] CallAgent history-derived result failed, fallback to wait payload: %v\n", historyErr)
	}

	fmt.Printf("[Gateway] CallAgent wait payload fallback: %v\n", waitPayload)
	return waitPayload, nil
}

func (c *GatewayClient) waitForAgentCompletion(gw *ws.Conn, runID string) (map[string]interface{}, error) {
	for {
		waitRequestID := fmt.Sprintf("wait-%d", time.Now().UnixNano())
		waitReq := map[string]interface{}{
			"type":   "req",
			"id":     waitRequestID,
			"method": "agent.wait",
			"params": map[string]interface{}{
				"runId":     runID,
				"timeoutMs": 60000,
			},
		}

		waitJSON, _ := json.MarshalIndent(waitReq, "", "  ")
		fmt.Printf("[Gateway] waitForAgentCompletion sending: %s\n", string(waitJSON))

		if err := gw.WriteJSON(waitReq); err != nil {
			return nil, fmt.Errorf("failed to send agent.wait: %w", err)
		}

		resp, err := c.readMatchingResponse(gw, waitRequestID)
		if err != nil {
			return nil, err
		}

		payload, ok := extractPayloadMap(resp)
		if !ok {
			return nil, fmt.Errorf("missing payload in agent.wait response: %v", resp)
		}

		status, _ := payload["status"].(string)
		fmt.Printf("[Gateway] waitForAgentCompletion status: %s\n", status)
		if status == "running" || status == "accepted" {
			continue
		}
		return payload, nil
	}
}

// GetSessionHistory calls GET /sessions/{sessionKey}/history on Gateway.
func (c *GatewayClient) GetSessionHistory(ctx context.Context, sessionKey string, query map[string]interface{}) (interface{}, error) {
	fmt.Printf("[Gateway] GetSessionHistory called with sessionKey=%s\n", sessionKey)

	url := c.baseURL + "/sessions/" + encodeURIComponent(sessionKey) + "/history"
	if len(query) > 0 {
		params := ""
		for k, v := range query {
			if v == nil || v == "" {
				continue
			}
			switch val := v.(type) {
			case string, int, bool:
				if params != "" {
					params += "&"
				}
				params += fmt.Sprintf("%s=%v", k, val)
			}
		}
		if params != "" {
			url += "?" + params
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setReadHeaders(req, sessionKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenClaw history failed: %d %s", resp.StatusCode, string(bodyBytes))
	}

	var result interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	fmt.Printf("[Gateway] GetSessionHistory success\n")
	return result, nil
}

// InvokeNode calls node.invoke via WebSocket on Gateway.
func (c *GatewayClient) InvokeNode(ctx context.Context, input interface{}) (interface{}, error) {
	fmt.Printf("[Gateway] InvokeNode called\n")

	inputJSON, _ := json.MarshalIndent(input, "", "  ")
	fmt.Printf("[Gateway] InvokeNode params: %s\n", string(inputJSON))

	invokeParams := convertToGatewayParams(input)

	gw, err := c.connectOperatorSocket(ctx)
	if err != nil {
		return nil, err
	}
	defer gw.Close()

	reqPayload := map[string]interface{}{
		"type":   "req",
		"id":     "invoke-1",
		"method": "node.invoke",
		"params": invokeParams,
	}

	reqJSON, _ := json.MarshalIndent(reqPayload, "", "  ")
	fmt.Printf("[Gateway] InvokeNode sending: %s\n", string(reqJSON))

	if err := gw.WriteJSON(reqPayload); err != nil {
		return nil, fmt.Errorf("failed to send invoke request: %w", err)
	}

	resp, err := c.readMatchingResponse(gw, "invoke-1")
	if err != nil {
		return nil, err
	}

	if payload, ok := extractPayloadMap(resp); ok {
		fmt.Printf("[Gateway] InvokeNode payload extracted: %v\n", payload)
		return payload, nil
	}
	if result, ok := resp["result"]; ok {
		fmt.Printf("[Gateway] InvokeNode result extracted: %v\n", result)
		return result, nil
	}

	fmt.Printf("[Gateway] InvokeNode raw frame fallback: %v\n", resp)
	return resp, nil
}

func (c *GatewayClient) connectOperatorSocket(ctx context.Context) (*ws.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, gatewayDialTimeout)
	defer cancel()

	gw, _, err := ws.DefaultDialer.DialContext(dialCtx, c.wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial gateway ws: %w", err)
	}

	_, rawMsg, err := gw.ReadMessage()
	if err != nil {
		gw.Close()
		return nil, fmt.Errorf("failed to read challenge: %w", err)
	}
	fmt.Printf("[Gateway] connectOperatorSocket first message: %s\n", string(rawMsg))

	var challenge map[string]interface{}
	if err := json.Unmarshal(rawMsg, &challenge); err != nil {
		gw.Close()
		return nil, fmt.Errorf("failed to parse challenge: %w", err)
	}
	challengeType, _ := challenge["type"].(string)
	eventName, _ := challenge["event"].(string)
	if challengeType != "event" || eventName != "connect.challenge" {
		gw.Close()
		return nil, fmt.Errorf("unexpected gateway handshake frame: %v", challenge)
	}

	connectReq := map[string]interface{}{
		"type":   "req",
		"id":     "connect-1",
		"method": "connect",
		"params": map[string]interface{}{
			"minProtocol": 3,
			"maxProtocol": 3,
			"client": map[string]interface{}{
				"id":       "cli",
				"version":  "1.0.0",
				"platform": "linux",
				"mode":     "cli",
			},
			"role":   "operator",
			"auth":   map[string]interface{}{"token": c.token},
			"scopes": []string{"operator.read", "operator.write"},
		},
	}

	fmt.Printf("[Gateway] connectOperatorSocket sending connect\n")
	if err := gw.WriteJSON(connectReq); err != nil {
		gw.Close()
		return nil, fmt.Errorf("failed to send connect: %w", err)
	}

	resp, err := c.readMatchingResponse(gw, "connect-1")
	if err != nil {
		gw.Close()
		return nil, err
	}
	fmt.Printf("[Gateway] connectOperatorSocket connected: %v\n", resp)
	return gw, nil
}

// transformToAgentParams transforms Hub input format to Gateway agent format.
func transformToAgentParams(input interface{}, sessionKey string) map[string]interface{} {
	result := make(map[string]interface{})
	if sessionKey != "" {
		result["sessionKey"] = sessionKey
	}
	result["idempotencyKey"] = fmt.Sprintf("agent-%d", time.Now().UnixNano())

	message, attachments := extractAgentMessageAndAttachments(input)
	if strings.TrimSpace(message) == "" && len(attachments) > 0 {
		message = "请查看附件并回答。"
	}
	result["message"] = message
	result["attachments"] = attachments
	return result
}

func extractAgentMessageAndAttachments(input interface{}) (string, []interface{}) {
	attachments := make([]interface{}, 0)
	textParts := make([]string, 0)

	appendText := func(text string) {
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			textParts = append(textParts, trimmed)
		}
	}

	appendTextFile := func(fileName, mimeType, data string) {
		decoded, ok := decodeBase64Text(data)
		if !ok {
			label := defaultString(fileName, "attachment")
			appendText(fmt.Sprintf("[附件 %s 无法直接解码为文本，mime=%s]", label, mimeType))
			return
		}
		label := defaultString(fileName, "attachment.txt")
		appendText(fmt.Sprintf("[文件 %s]\n%s", label, decoded))
	}

	var walkContent func(content interface{})
	walkContent = func(content interface{}) {
		switch current := content.(type) {
		case string:
			appendText(current)
		case []interface{}:
			for _, item := range current {
				walkContent(item)
			}
		case map[string]interface{}:
			partType, _ := current["type"].(string)
			switch partType {
			case "input_text", "text":
				if text, ok := current["text"].(string); ok {
					appendText(text)
				}
			case "input_image":
				if attachment, ok := buildImageAttachment(current); ok {
					attachments = append(attachments, attachment)
				}
			case "input_file":
				source, ok := current["source"].(map[string]interface{})
				if !ok {
					return
				}
				sourceType, _ := source["type"].(string)
				if sourceType != "base64" {
					return
				}
				mimeType, _ := source["media_type"].(string)
				fileName, _ := source["filename"].(string)
				data, _ := source["data"].(string)
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") {
					attachment := map[string]interface{}{
						"type":     "image",
						"mimeType": mimeType,
						"fileName": defaultString(fileName, "image-file"),
						"content":  stripDataURLPrefix(data),
					}
					attachments = append(attachments, attachment)
					return
				}
				appendTextFile(fileName, mimeType, data)
			case "message":
				if nested, ok := current["content"]; ok {
					walkContent(nested)
				}
			default:
				if nested, ok := current["content"]; ok {
					walkContent(nested)
					return
				}
				if text, ok := current["text"].(string); ok {
					appendText(text)
				}
			}
		default:
			appendText(fmt.Sprintf("%v", current))
		}
	}

	switch current := input.(type) {
	case string:
		appendText(current)
	case map[string]interface{}:
		if message, ok := current["message"].(string); ok {
			appendText(message)
		}
		if text, ok := current["text"].(string); ok {
			appendText(text)
		}
		if rawInput, ok := current["input"]; ok {
			walkContent(rawInput)
		}
		if content, ok := current["content"]; ok {
			walkContent(content)
		}
	case []interface{}:
		walkContent(current)
	default:
		appendText(fmt.Sprintf("%v", current))
	}

	return strings.Join(textParts, "\n\n"), attachments
}

func buildImageAttachment(part map[string]interface{}) (map[string]interface{}, bool) {
	source, ok := part["source"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	sourceType, _ := source["type"].(string)
	if sourceType != "base64" {
		return nil, false
	}
	mimeType, _ := source["media_type"].(string)
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") {
		return nil, false
	}
	data, _ := source["data"].(string)
	fileName, _ := source["filename"].(string)
	return map[string]interface{}{
		"type":     "image",
		"mimeType": mimeType,
		"fileName": defaultString(fileName, "image"),
		"content":  stripDataURLPrefix(data),
	}, true
}

// convertToGatewayParams converts Hub input format to Gateway format.
func convertToGatewayParams(input interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	inputMap, ok := input.(map[string]interface{})
	if !ok {
		if m, ok := input.(map[string]interface{}); ok {
			return m
		}
		return result
	}

	if node, ok := inputMap["node"].(string); ok {
		parts := strings.SplitN(node, ".", 2)
		if len(parts) >= 1 {
			result["nodeId"] = parts[0]
		}
		if len(parts) == 2 {
			result["command"] = parts[1]
		}
	}
	if params, ok := inputMap["params"].(map[string]interface{}); ok {
		result["params"] = params
	}
	if _, exists := inputMap["idempotencyKey"]; !exists {
		result["idempotencyKey"] = fmt.Sprintf("invoke-%d", time.Now().UnixNano())
	}
	for k, v := range inputMap {
		if k != "node" && k != "params" {
			result[k] = v
		}
	}
	return result
}

// readMatchingResponse reads WebSocket messages until it finds a response matching the given request ID.
// 这里必须忽略 health / tick 之类 event，不然会把事件误当成业务返回。
func (c *GatewayClient) readMatchingResponse(gw *ws.Conn, requestID string) (map[string]interface{}, error) {
	for {
		_, rawMsg, err := gw.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		fmt.Printf("[Gateway] readMatchingResponse received: %s\n", string(rawMsg))

		var msg map[string]interface{}
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}

		msgType, _ := msg["type"].(string)
		msgID, _ := msg["id"].(string)
		if msgType == "event" {
			fmt.Printf("[Gateway] readMatchingResponse skipping event: %v\n", msg["event"])
			continue
		}
		if msgType == "res" && msgID == requestID {
			if errVal, hasError := msg["error"]; hasError && errVal != nil {
				return nil, fmt.Errorf("gateway error: %v", errVal)
			}
			return msg, nil
		}
		fmt.Printf("[Gateway] readMatchingResponse: got unexpected msg type=%s id=%s, continue\n", msgType, msgID)
	}
}

func extractPayloadMap(frame map[string]interface{}) (map[string]interface{}, bool) {
	if frame == nil {
		return nil, false
	}
	payload, ok := frame["payload"].(map[string]interface{})
	return payload, ok
}

// NormalizeNodeArtifacts extracts artifacts from node.invoke result.
func (c *GatewayClient) NormalizeNodeArtifacts(result interface{}) (interface{}, []protocol.NormalizedArtifact) {
	if result == nil {
		fmt.Printf("[Gateway] NormalizeNodeArtifacts: result is nil\n")
		return nil, nil
	}

	resultJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("[Gateway] NormalizeNodeArtifacts: result=%s\n", string(resultJSON))

	record, ok := result.(map[string]interface{})
	if !ok {
		return result, nil
	}

	targetRecord := record
	if payloadMap, ok := record["payload"].(map[string]interface{}); ok {
		targetRecord = payloadMap
	}

	base64Str, ok := targetRecord["base64"].(string)
	if !ok || base64Str == "" {
		return result, nil
	}

	format := "bin"
	if f, ok := targetRecord["format"].(string); ok {
		format = f
	}

	mimeType := ""
	if mt, ok := targetRecord["mimeType"].(string); ok && mt != "" {
		mimeType = mt
	} else {
		mimeType = mimeTypeFromFormat(format)
	}

	delete(targetRecord, "base64")

	kind := detectArtifactKind(mimeType)
	ext := extFromFormat(format)
	filename := "node-output." + ext

	return record, []protocol.NormalizedArtifact{{
		Kind:          kind,
		Filename:      filename,
		MimeType:      mimeType,
		ContentBase64: base64Str,
		Meta: map[string]interface{}{
			"format": format,
		},
	}}
}

func (c *GatewayClient) getLatestHistorySeq(ctx context.Context, sessionKey string) (int, error) {
	if sessionKey == "" {
		return 0, nil
	}
	result, err := c.GetSessionHistory(ctx, sessionKey, map[string]interface{}{"limit": 5})
	if err != nil {
		return 0, err
	}
	historyMap, ok := result.(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("unexpected history response type: %T", result)
	}
	items, ok := historyMap["items"].([]interface{})
	if !ok {
		return 0, nil
	}
	latest := 0
	for _, item := range items {
		seq := extractHistorySeq(item)
		if seq > latest {
			latest = seq
		}
	}
	return latest, nil
}

func (c *GatewayClient) buildAgentResultFromHistory(ctx context.Context, sessionKey string, baselineSeq int) (interface{}, error) {
	history, err := c.GetSessionHistory(ctx, sessionKey, map[string]interface{}{"limit": 20})
	if err != nil {
		return nil, err
	}
	historyMap, ok := history.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected history response type: %T", history)
	}
	items, ok := historyMap["items"].([]interface{})
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("history result missing items")
	}

	var latestAssistant map[string]interface{}
	latestSeq := 0
	for _, item := range items {
		record, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := record["role"].(string)
		if role != "assistant" {
			continue
		}
		seq := extractHistorySeq(record)
		if seq < baselineSeq {
			continue
		}
		if latestAssistant == nil || seq >= latestSeq {
			latestAssistant = record
			latestSeq = seq
		}
	}
	if latestAssistant == nil {
		return nil, fmt.Errorf("no assistant entry found after seq=%d", baselineSeq)
	}

	text := extractAssistantText(latestAssistant)
	payload := map[string]interface{}{
		"payloads": []interface{}{map[string]interface{}{"text": text}},
	}
	if usage, ok := latestAssistant["usage"]; ok {
		payload["meta"] = map[string]interface{}{"usage": usage}
	}
	return payload, nil
}

func extractHistorySeq(item interface{}) int {
	record, ok := item.(map[string]interface{})
	if !ok {
		return 0
	}
	meta, ok := record["__openclaw"].(map[string]interface{})
	if !ok {
		return 0
	}
	seqFloat, ok := meta["seq"].(float64)
	if !ok {
		return 0
	}
	return int(seqFloat)
}

func extractAssistantText(record map[string]interface{}) string {
	content, ok := record["content"].([]interface{})
	if !ok {
		return ""
	}
	texts := make([]string, 0)
	for _, block := range content {
		part, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := part["type"].(string)
		if blockType != "text" {
			continue
		}
		text, _ := part["text"].(string)
		if strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// Helper functions

func (c *GatewayClient) setHeaders(req *http.Request, scope, contentType, sessionKey string) {
	req.Header.Set("Content-Type", contentType)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if scope != "" {
		req.Header.Set("x-openclaw-scopes", scope)
	}
	if sessionKey != "" {
		req.Header.Set("X-OpenClaw-Session-Key", sessionKey)
	}
}

func (c *GatewayClient) setReadHeaders(req *http.Request, sessionKey string) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("x-openclaw-scopes", operatorReadScope)
	if sessionKey != "" {
		req.Header.Set("X-OpenClaw-Session-Key", sessionKey)
	}
}

func detectArtifactKind(mimeType string) string {
	switch {
	case len(mimeType) >= 6 && mimeType[:6] == "image/":
		return "image"
	case len(mimeType) >= 6 && mimeType[:6] == "video/":
		return "video"
	case len(mimeType) >= 6 && mimeType[:6] == "audio/":
		return "audio"
	default:
		return "file"
	}
}

func mimeTypeFromFormat(format string) string {
	switch format {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "mp4":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

func extFromFormat(format string) string {
	switch format {
	case "jpg", "jpeg":
		return "jpg"
	case "png":
		return "png"
	case "mp4":
		return "mp4"
	default:
		return format
	}
}

func encodeURIComponent(s string) string {
	result := ""
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~':
			result += string(c)
		default:
			result += fmt.Sprintf("%%%02X", c)
		}
	}
	return result
}

func stripDataURLPrefix(data string) string {
	trimmed := strings.TrimSpace(data)
	if idx := strings.Index(trimmed, ","); strings.HasPrefix(strings.ToLower(trimmed), "data:") && idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}

func decodeBase64Text(data string) (string, bool) {
	raw := stripDataURLPrefix(data)
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil || !utf8.Valid(decoded) {
		return "", false
	}
	return string(decoded), true
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
