package gatewayclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/funclaw/go-worker/internal/protocol"
	ws "github.com/gorilla/websocket"
)

const (
	operatorReadScope  = "operator.read"
	operatorWriteScope = "operator.write"
	gatewayDialTimeout = 10 * time.Second
	invokeTimeout      = 30 * time.Second
)

// GatewayClient is a client for calling OpenClaw Gateway
type GatewayClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	wsURL      string
}

// New creates a new Gateway client
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

// CallAgent calls the agent method via WebSocket on Gateway
func (c *GatewayClient) CallAgent(ctx context.Context, input interface{}, sessionKey string) (interface{}, error) {
	fmt.Printf("[Gateway] CallAgent called with sessionKey=%s\n", sessionKey)

	inputJSON, _ := json.MarshalIndent(input, "", "  ")
	fmt.Printf("[Gateway] CallAgent input: %s\n", string(inputJSON))

	dialCtx, cancel := context.WithTimeout(ctx, gatewayDialTimeout)
	defer cancel()

	gw, _, err := ws.DefaultDialer.DialContext(dialCtx, c.wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial gateway ws: %w", err)
	}
	defer gw.Close()

	// Read first message - should be connect.challenge
	_, rawMsg, err := gw.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("failed to read challenge: %w", err)
	}
	fmt.Printf("[Gateway] CallAgent first message: %s\n", string(rawMsg))

	var challenge map[string]interface{}
	if err := json.Unmarshal(rawMsg, &challenge); err != nil {
		return nil, fmt.Errorf("failed to parse challenge: %w", err)
	}

	// Handle connect.challenge
	if challengeType, ok := challenge["type"].(string); ok && challengeType == "event" {
		if event, ok := challenge["event"].(string); ok && event == "connect.challenge" {
			fmt.Printf("[Gateway] Received connect.challenge\n")

			connectResp := map[string]interface{}{
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

			fmt.Printf("[Gateway] Sending connect\n")
			if err := gw.WriteJSON(connectResp); err != nil {
				return nil, fmt.Errorf("failed to send connect: %w", err)
			}

			// Read response
			_, rawMsg, err = gw.ReadMessage()
			if err != nil {
				return nil, fmt.Errorf("failed to read connect response: %w", err)
			}
			fmt.Printf("[Gateway] Connect response: %s\n", string(rawMsg))

			var resp map[string]interface{}
			json.Unmarshal(rawMsg, &resp)

			if ok, hasOK := resp["ok"].(bool); !hasOK || !ok {
				errMsg := "connect failed"
				if errVal, hasErr := resp["error"].(map[string]interface{}); hasErr {
					if msg, hasMsg := errVal["message"].(string); hasMsg {
						errMsg = msg
					}
				}
				return nil, fmt.Errorf("[Gateway] %s", errMsg)
			}
			fmt.Printf("[Gateway] Connect succeeded!\n")
		}
	}

	// Transform input to agent format
	agentParams := transformToAgentParams(input, sessionKey)

	// Now send the agent request
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

	// Read response - should be accepted (async agent)
	initialResp, err := c.readMatchingResponse(gw, "agent-1")
	if err != nil {
		return nil, err
	}

	// Extract runId from accepted response
	initialMap, ok := initialResp.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected agent response type: %T", initialResp)
	}

	payload, ok := initialMap["payload"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing payload in agent response")
	}

	runId, ok := payload["runId"].(string)
	if !ok {
		return nil, fmt.Errorf("missing runId in agent response: %v", payload)
	}

	status, _ := payload["status"].(string)
	fmt.Printf("[Gateway] CallAgent accepted, runId=%s, status=%s\n", runId, status)

	// Poll agent.wait until the task completes
	result, err := c.waitForAgentResult(gw, runId)
	if err != nil {
		return nil, err
	}

	fmt.Printf("[Gateway] CallAgent result extracted: %v\n", result)
	return result, nil
}

// waitForAgentResult polls agent.wait until the task completes or times out
func (c *GatewayClient) waitForAgentResult(gw *ws.Conn, runId string) (interface{}, error) {
	waitReq := map[string]interface{}{
		"type":   "req",
		"id":     "wait-1",
		"method": "agent.wait",
		"params": map[string]interface{}{
			"runId":     runId,
			"timeoutMs": 60000,
		},
	}

	waitJSON, _ := json.MarshalIndent(waitReq, "", "  ")
	fmt.Printf("[Gateway] waitForAgentResult sending: %s\n", string(waitJSON))

	if err := gw.WriteJSON(waitReq); err != nil {
		return nil, fmt.Errorf("failed to send agent.wait: %w", err)
	}

	// Read response
	resp, err := c.readMatchingResponse(gw, "wait-1")
	if err != nil {
		return nil, err
	}

	respMap, ok := resp.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected wait response type: %T", resp)
	}

	respPayload, ok := respMap["payload"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing payload in agent.wait response")
	}

	status, _ := respPayload["status"].(string)
	fmt.Printf("[Gateway] waitForAgentResult status: %s\n", status)

	// If completed, return the result
	if status == "completed" {
		if result, ok := respPayload["result"]; ok {
			return result, nil
		}
		// Some completed responses put result at the top level of payload
		delete(respPayload, "status")
		return respPayload, nil
	}

	// If still running, poll again
	if status == "running" || status == "accepted" {
		return c.waitForAgentResult(gw, runId)
	}

	return respPayload, nil
}

// transformToAgentParams transforms Hub input format to Gateway agent format
func transformToAgentParams(input interface{}, sessionKey string) map[string]interface{} {
	result := make(map[string]interface{})

	// Set sessionId if provided
	if sessionKey != "" {
		result["sessionId"] = sessionKey
	}

	// Generate idempotencyKey
	result["idempotencyKey"] = fmt.Sprintf("agent-%d", time.Now().UnixNano())

	// Initialize attachments as empty array
	result["attachments"] = []interface{}{}

	switch v := input.(type) {
	case string:
		result["message"] = v
	case map[string]interface{}:
		// Look for common message field names
		if msg, ok := v["message"].(string); ok {
			result["message"] = msg
		} else if text, ok := v["text"].(string); ok {
			result["message"] = text
		} else if inp, ok := v["input"].(string); ok {
			result["message"] = inp
		} else {
			// Pass through as message if it's a map
			result["message"] = fmt.Sprintf("%v", v)
		}
	default:
		result["message"] = fmt.Sprintf("%v", input)
	}

	return result
}

// GetSessionHistory calls GET /sessions/{sessionKey}/history on Gateway
func (c *GatewayClient) GetSessionHistory(ctx context.Context, sessionKey string, query map[string]interface{}) (interface{}, error) {
	fmt.Printf("[Gateway] GetSessionHistory called with sessionKey=%s\n", sessionKey)

	url := c.baseURL + "/sessions/" + encodeURIComponent(sessionKey) + "/history"

	// Add query params
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

// InvokeNode calls node.invoke via WebSocket on Gateway
func (c *GatewayClient) InvokeNode(ctx context.Context, input interface{}) (interface{}, error) {
	fmt.Printf("[Gateway] InvokeNode called\n")

	// Debug: print input params
	inputJSON, _ := json.MarshalIndent(input, "", "  ")
	fmt.Printf("[Gateway] InvokeNode params: %s\n", string(inputJSON))

	// Convert Hub input format (node: "canvas.snapshot", idempotencyKey: "xxx")
	// to Gateway format (nodeId: "canvas", command: "snapshot", idempotencyKey: "xxx")
	invokeParams := convertToGatewayParams(input)

	dialCtx, cancel := context.WithTimeout(ctx, gatewayDialTimeout)
	defer cancel()

	gw, _, err := ws.DefaultDialer.DialContext(dialCtx, c.wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial gateway ws: %w", err)
	}
	defer gw.Close()

	// Read first message - should be connect.challenge
	_, rawMsg, err := gw.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("failed to read challenge: %w", err)
	}
	fmt.Printf("[Gateway] InvokeNode first message: %s\n", string(rawMsg))

	var challenge map[string]interface{}
	if err := json.Unmarshal(rawMsg, &challenge); err != nil {
		return nil, fmt.Errorf("failed to parse challenge: %w", err)
	}

	// Handle connect.challenge
	if challengeType, ok := challenge["type"].(string); ok && challengeType == "event" {
		if event, ok := challenge["event"].(string); ok && event == "connect.challenge" {
			fmt.Printf("[Gateway] Received connect.challenge\n")

			// The correct connect params for OpenClaw Gateway (found via testing):
			// - id: "cli"
			// - mode: "cli"
			// - role: "operator"
			// - minProtocol/maxProtocol: 3
			// - auth.token: gateway token
			// - scopes: ["operator.read", "operator.write"]
			connectResp := map[string]interface{}{
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

			fmt.Printf("[Gateway] Sending connect with id=cli, mode=cli, role=operator, protocol=3, scopes\n")
			if err := gw.WriteJSON(connectResp); err != nil {
				return nil, fmt.Errorf("failed to send connect: %w", err)
			}

			// Read response
			_, rawMsg, err = gw.ReadMessage()
			if err != nil {
				return nil, fmt.Errorf("failed to read connect response: %w", err)
			}
			fmt.Printf("[Gateway] Connect response: %s\n", string(rawMsg))

			var resp map[string]interface{}
			json.Unmarshal(rawMsg, &resp)

			if ok, hasOK := resp["ok"].(bool); !hasOK || !ok {
				errMsg := "connect failed"
				if errVal, hasErr := resp["error"].(map[string]interface{}); hasErr {
					if msg, hasMsg := errVal["message"].(string); hasMsg {
						errMsg = msg
					}
				}
				return nil, fmt.Errorf("[Gateway] %s", errMsg)
			}
			fmt.Printf("[Gateway] Connect succeeded!\n")
		}
	}

	// Now send the invoke request
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

	// Read response - must match by id and type to get the correct response
	result, err := c.readMatchingResponse(gw, "invoke-1")
	if err != nil {
		return nil, err
	}

	fmt.Printf("[Gateway] InvokeNode result extracted: %v\n", result)
	return result, nil
}

// convertToGatewayParams converts Hub input format to Gateway format
// Hub: {node: "canvas.snapshot", params: {arg1: "val1"}, timeoutMs: 30}
// Gateway: {nodeId: "canvas", command: "snapshot", params: {arg1: "val1"}, timeoutMs: 30}
func convertToGatewayParams(input interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	inputMap, ok := input.(map[string]interface{})
	if !ok {
		// If not a map, just return as-is
		if m, ok := input.(map[string]interface{}); ok {
			return m
		}
		return result
	}

	// Convert "node" to nodeId + command if present
	if node, ok := inputMap["node"].(string); ok {
		parts := strings.SplitN(node, ".", 2)
		if len(parts) >= 1 {
			result["nodeId"] = parts[0]
		}
		if len(parts) == 2 {
			result["command"] = parts[1]
		}
	}

	// Preserve params as an object (do not flatten)
	if params, ok := inputMap["params"].(map[string]interface{}); ok {
		result["params"] = params
	}

	// Add idempotencyKey for the Gateway
	result["idempotencyKey"] = fmt.Sprintf("invoke-%d", time.Now().UnixNano())

	// Copy over any other fields that aren't node or params
	for k, v := range inputMap {
		if k != "node" && k != "params" {
			result[k] = v
		}
	}

	return result
}

// readMatchingResponse reads WebSocket messages until it finds a response matching the given request ID.
// It ignores event messages (like health events) that arrive before the actual response.
func (c *GatewayClient) readMatchingResponse(gw *ws.Conn, requestID string) (interface{}, error) {
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

		// Skip event messages (like health) - they don't have matching id
		if msgType == "event" {
			fmt.Printf("[Gateway] readMatchingResponse skipping event: %v\n", msg["event"])
			continue
		}

		// Check if this is a response for our request
		if msgType == "res" && msgID == requestID {
			// Check for error field
			if errVal, hasError := msg["error"]; hasError && errVal != nil {
				return nil, fmt.Errorf("gateway error: %v", errVal)
			}

			// Extract result
			if result, hasResult := msg["result"]; hasResult {
				return result, nil
			}
			// If no result field but no error, return the whole response
			delete(msg, "type")
			delete(msg, "id")
			return msg, nil
		}

		// Not our response, keep reading
		fmt.Printf("[Gateway] readMatchingResponse: got unexpected msg type=%s id=%s, continue\n", msgType, msgID)
	}
}

// NormalizeNodeArtifacts extracts artifacts from node.invoke result
func (c *GatewayClient) NormalizeNodeArtifacts(result interface{}) (interface{}, []protocol.NormalizedArtifact) {
	if result == nil {
		fmt.Printf("[Gateway] NormalizeNodeArtifacts: result is nil\n")
		return nil, nil
	}

	// Debug: print the raw result structure
	resultJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("[Gateway] NormalizeNodeArtifacts: result=%s\n", string(resultJSON))

	record, ok := result.(map[string]interface{})
	if !ok {
		return result, nil
	}

	base64Str, ok := record["base64"].(string)
	if !ok || base64Str == "" {
		return result, nil
	}

	format := "bin"
	if f, ok := record["format"].(string); ok {
		format = f
	}

	mimeType := ""
	if mt, ok := record["mimeType"].(string); ok && mt != "" {
		mimeType = mt
	} else {
		mimeType = mimeTypeFromFormat(format)
	}

	// Remove base64 from result
	delete(record, "base64")

	kind := detectArtifactKind(mimeType)
	ext := extFromFormat(format)
	filename := "node-output." + ext

	return record, []protocol.NormalizedArtifact{
		{
			Kind:          kind,
			Filename:      filename,
			MimeType:      mimeType,
			ContentBase64: base64Str,
			Meta: map[string]interface{}{
				"format": format,
			},
		},
	}
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
	// Simple encoding for session keys
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
