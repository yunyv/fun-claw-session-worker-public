package hubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/funclaw/go-worker/internal/constants"
	"github.com/funclaw/go-worker/internal/protocol"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// HubFrame is a generic frame that can be any type
type HubFrame struct {
	Type    protocol.FrameType   `json:"type"`
	ID      string               `json:"id"`
	Method  string               `json:"method,omitempty"`
	Event   string               `json:"event,omitempty"`
	OK      bool                 `json:"ok,omitempty"`
	Payload interface{}          `json:"payload,omitempty"`
	Error   *protocol.ErrorShape `json:"error,omitempty"`
	Seq     *int                 `json:"seq,omitempty"`
}

// HubClientOptions contains options for creating a Hub client
type HubClientOptions struct {
	URL           string
	Token         string
	WorkerID      string
	Hostname      string
	Version       string
	Capabilities  []string
	OnTaskAssigned func(*protocol.TaskAssignedPayload) error
}

// HubClient is a client for connecting to the Session Hub
type HubClient struct {
	ws              *websocket.Conn
	opts            HubClientOptions
	pending         map[string]chan *protocol.HubResponseFrame
	pendingMu       sync.RWMutex
	closed          bool
	heartbeatTicker *time.Ticker
	connectTimer    *time.Timer
	nonce           string
	connected       chan struct{}
	writeMu         sync.Mutex
	reconnecting    bool
}

// HubResponse represents a response from Hub
type HubResponse struct {
	OK      bool
	Payload interface{}
	Error   *protocol.ErrorShape
}

// New creates a new Hub client
func New(opts HubClientOptions) *HubClient {
	return &HubClient{
		opts:      opts,
		pending:   make(map[string]chan *protocol.HubResponseFrame),
		connected: make(chan struct{}),
	}
}

// Start connects to the Hub and starts the main loop
func (c *HubClient) Start(ctx context.Context) error {
	if err := c.connect(ctx); err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	go c.readLoop()
	return nil
}

// Stop gracefully stops the Hub client
func (c *HubClient) Stop() {
	c.closed = true
	if c.heartbeatTicker != nil {
		c.heartbeatTicker.Stop()
		c.heartbeatTicker = nil
	}
	if c.connectTimer != nil {
		c.connectTimer.Stop()
		c.connectTimer = nil
	}
	if c.ws != nil {
		c.ws.Close()
	}
}

// SendHeartbeat sends a heartbeat to the Hub
func (c *HubClient) SendHeartbeat() error {
	_, err := c.request("worker.heartbeat", protocol.WorkerHeartbeatPayload{
		WorkerID: c.opts.WorkerID,
		TS:       time.Now().UTC(),
	})
	return err
}

// SendAccepted sends a task.accepted message
func (c *HubClient) SendAccepted(requestID string) error {
	_, err := c.request("task.accepted", protocol.TaskAcceptedPayload{
		RequestID:  requestID,
		AcceptedAt: time.Now().UTC(),
	})
	return err
}

// SendCompleted sends a task.completed message
func (c *HubClient) SendCompleted(requestID string, result interface{}, artifacts []protocol.ArtifactDescriptor) error {
	_, err := c.request("task.completed", protocol.TaskCompletedPayload{
		RequestID:   requestID,
		CompletedAt: time.Now().UTC(),
		Result:      result,
		Artifacts:   artifacts,
	})
	return err
}

// SendFailed sends a task.failed message
func (c *HubClient) SendFailed(requestID string, errShape protocol.ErrorShape) error {
	_, err := c.request("task.failed", protocol.TaskFailedPayload{
		RequestID: requestID,
		FailedAt:  time.Now().UTC(),
		Error:     errShape,
	})
	return err
}

// RegisterArtifact registers an artifact with the Hub
func (c *HubClient) RegisterArtifact(params protocol.ArtifactRegisterParams) (*protocol.ArtifactDescriptor, error) {
	resp, err := c.request("artifact.register", params)
	if err != nil {
		return nil, err
	}

	// resp is a map[string]interface{} from JSON decoding, not *protocol.ArtifactDescriptor
	respMap, ok := resp.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected artifact.register response type: %T", resp)
	}

	// Build ArtifactDescriptor from map
	descriptor := &protocol.ArtifactDescriptor{}

	if id, ok := respMap["artifact_id"].(string); ok {
		descriptor.ArtifactID = id
	}
	if kind, ok := respMap["kind"].(string); ok {
		descriptor.Kind = protocol.ArtifactKind(kind)
	}
	if filename, ok := respMap["filename"].(string); ok {
		descriptor.Filename = filename
	}
	if mimeType, ok := respMap["mime_type"].(string); ok {
		descriptor.MimeType = mimeType
	}
	if sizeBytes, ok := respMap["size_bytes"].(float64); ok {
		descriptor.SizeBytes = int(sizeBytes)
	}
	if sha256, ok := respMap["sha256"].(string); ok {
		descriptor.SHA256 = sha256
	}
	if transport, ok := respMap["transport"].(string); ok {
		descriptor.Transport = protocol.ArtifactTransport(transport)
	}
	if inlineBase64, ok := respMap["inline_base64"].(string); ok {
		descriptor.InlineBase64 = &inlineBase64
	}
	if downloadURL, ok := respMap["download_url"].(string); ok {
		descriptor.DownloadURL = &downloadURL
	}
	if meta, ok := respMap["meta"].(map[string]interface{}); ok {
		descriptor.Meta = meta
	}

	return descriptor, nil
}

func (c *HubClient) connect(ctx context.Context) error {
	ws, _, err := websocket.DefaultDialer.DialContext(ctx, c.opts.URL, nil)
	if err != nil {
		return fmt.Errorf("dial error: %w", err)
	}
	c.ws = ws

	// Wait for connect.challenge with timeout
	c.connectTimer = time.AfterFunc(time.Duration(constants.HubDefaultConnectTimeoutMs)*time.Millisecond, func() {
		ws.Close()
	})

	return nil
}

func (c *HubClient) readLoop() {
	for {
		if c.closed {
			return
		}
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			if c.closed {
				return
			}
			// Reconnect
			go func() {
				time.Sleep(time.Second)
				ctx := context.Background()
				if err := c.connect(ctx); err != nil {
					return
				}
				go c.readLoop()
			}()
			return
		}

		var frame HubFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}

		switch frame.Type {
		case protocol.FrameTypeEvent:
			c.handleEvent(frame)
		case protocol.FrameTypeRes:
			c.handleResponse(frame)
		}
	}
}

func (c *HubClient) handleEvent(frame HubFrame) {
	event, ok := frame.Payload.(map[string]interface{})
	if !ok {
		return
	}

	switch frame.Event {
	case "connect.challenge":
		nonceVal, ok := event["nonce"].(string)
		if !ok || nonceVal == "" {
			fmt.Println("Hub connect.challenge missing nonce")
			return
		}
		c.nonce = nonceVal
		c.sendConnect()

	case "task.assigned":
		payloadBytes, err := json.Marshal(event)
		if err != nil {
			fmt.Printf("failed to marshal task.assigned payload: %v\n", err)
			return
		}
		var payload protocol.TaskAssignedPayload
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			fmt.Printf("failed to unmarshal task.assigned payload: %v\n", err)
			return
		}
		if c.opts.OnTaskAssigned != nil {
			go func() {
				if err := c.opts.OnTaskAssigned(&payload); err != nil {
					fmt.Printf("task handler error: %v\n", err)
				}
			}()
		}
	}
}

func (c *HubClient) handleResponse(frame HubFrame) {
	// Check if this is a hello-ok response to start heartbeat
	if frame.ID == "connect-1" && frame.OK && frame.Payload != nil {
		c.handleHelloOk(frame.Payload)
	}

	c.pendingMu.RLock()
	ch, ok := c.pending[frame.ID]
	c.pendingMu.RUnlock()
	if !ok {
		return
	}

	c.pendingMu.Lock()
	delete(c.pending, frame.ID)
	c.pendingMu.Unlock()

	var resp protocol.HubResponseFrame
	respBytes, _ := json.Marshal(frame)
	json.Unmarshal(respBytes, &resp)

	select {
	case ch <- &resp:
	default:
	}
}

func (c *HubClient) handleHelloOk(payload interface{}) {
	// Clear the connect timeout timer
	if c.connectTimer != nil {
		c.connectTimer.Stop()
		c.connectTimer = nil
	}

	// Extract heartbeat interval from hello-ok
	intervalMs := constants.HubDefaultHeartbeatIntervalMs
	if payloadMap, ok := payload.(map[string]interface{}); ok {
		if policy, ok := payloadMap["policy"].(map[string]interface{}); ok {
			if hbMs, ok := policy["heartbeatIntervalMs"].(float64); ok {
				intervalMs = int(hbMs)
			}
		}
	}

	// Signal that we're connected
	select {
	case c.connected <- struct{}{}:
	default:
	}

	// Start heartbeat
	c.startHeartbeat(intervalMs)

	fmt.Printf("Hub client connected, heartbeat interval: %dms\n", intervalMs)
}

func (c *HubClient) startHeartbeat(intervalMs int) {
	// Stop existing ticker if any
	if c.heartbeatTicker != nil {
		c.heartbeatTicker.Stop()
	}

	c.heartbeatTicker = time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	go func() {
		for {
			if c.closed {
				return
			}
			select {
			case <-c.heartbeatTicker.C:
				if err := c.SendHeartbeat(); err != nil {
					fmt.Printf("heartbeat error: %v\n", err)
				}
			}
		}
	}()
}

func (c *HubClient) sendConnect() {
	params := protocol.HubConnectParams{
		MinProtocol: constants.HubProtocolVersion,
		MaxProtocol: constants.HubProtocolVersion,
		Client: protocol.HubClientInfo{
			ID:       "funclaw-worker",
			Version:  c.opts.Version,
			Platform: "linux", // TODO: detect platform
			Mode:     "worker",
		},
		Role:  "worker",
		Nonce: c.nonce,
		Worker: &protocol.WorkerRegistration{
			WorkerID:     c.opts.WorkerID,
			Hostname:     c.opts.Hostname,
			Version:      c.opts.Version,
			Capabilities: c.opts.Capabilities,
		},
	}

	if c.opts.Token != "" {
		params.Auth = &protocol.HubAuth{
			Token: &c.opts.Token,
		}
	}

	// Send connect request synchronously to get hello-ok
	go func() {
		_, err := c.request("connect", params)
		if err != nil {
			fmt.Printf("connect error: %v\n", err)
		}
	}()
}

func (c *HubClient) request(method string, params interface{}) (interface{}, error) {
	id := uuid.New().String()

	// Use "connect-1" as the ID for connect requests so we can identify hello-ok
	reqID := id
	if method == "connect" {
		reqID = "connect-1"
	}

	reqFrame := protocol.HubRequestFrame{
		Type:   protocol.FrameTypeReq,
		ID:     reqID,
		Method: method,
		Params: params,
	}

	reqBytes, err := json.Marshal(reqFrame)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	ch := make(chan *protocol.HubResponseFrame, 1)
	c.pendingMu.Lock()
	c.pending[reqID] = ch
	c.pendingMu.Unlock()

	c.writeMu.Lock()
	err = c.ws.WriteMessage(websocket.TextMessage, reqBytes)
	c.writeMu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-ch:
		if !resp.OK {
			return nil, fmt.Errorf("hub request failed: %v", resp.Error)
		}
		return resp.Payload, nil
	case <-time.After(time.Duration(constants.HubDefaultConnectTimeoutMs) * time.Millisecond):
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("hub request timeout for %s", method)
	}
}
