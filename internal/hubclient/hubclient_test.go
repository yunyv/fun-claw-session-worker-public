package hubclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/funclaw/go-worker/internal/protocol"
)

func TestHubClientOptions(t *testing.T) {
	opts := HubClientOptions{
		URL:      "ws://localhost:31880/ws",
		Token:    "test-token",
		WorkerID: "worker-1",
		Hostname: "test-host",
		Version:  "1.0.0",
		Capabilities: []string{"responses.create", "session.history.get"},
		OnTaskAssigned: func(task *protocol.TaskAssignedPayload) error {
			return nil
		},
	}

	if opts.WorkerID != "worker-1" {
		t.Errorf("expected WorkerID=worker-1, got %s", opts.WorkerID)
	}
	if len(opts.Capabilities) != 2 {
		t.Errorf("expected 2 capabilities, got %d", len(opts.Capabilities))
	}
}

func TestHubFrame_JSON(t *testing.T) {
	frame := HubFrame{
		Type:    protocol.FrameTypeReq,
		ID:      "test-id",
		Method:  "connect",
		Payload: map[string]interface{}{"nonce": "abc123"},
	}

	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded HubFrame
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Type != protocol.FrameTypeReq {
		t.Errorf("expected type=req, got %s", decoded.Type)
	}
	if decoded.ID != "test-id" {
		t.Errorf("expected id=test-id, got %s", decoded.ID)
	}
}

func TestHubResponseFrame_JSON(t *testing.T) {
	frame := protocol.HubResponseFrame{
		Type:    protocol.FrameTypeRes,
		ID:      "test-id",
		OK:      true,
		Payload: map[string]string{"status": "ok"},
	}

	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded protocol.HubResponseFrame
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if !decoded.OK {
		t.Error("expected ok=true")
	}
}

func TestTaskAssignedPayload_JSON(t *testing.T) {
	payload := protocol.TaskAssignedPayload{
		RequestID:           "req-123",
		SessionID:          "session-1",
		WorkerID:           "worker-1",
		AdapterID:          "adapter-1",
		OpenclawSessionKey: "openclaw:session-1",
		Action:             protocol.TaskActionResponsesCreate,
		Input:              map[string]string{"input": "hello"},
		CreatedAt:          time.Now(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded protocol.TaskAssignedPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.RequestID != "req-123" {
		t.Errorf("expected request_id=req-123, got %s", decoded.RequestID)
	}
	if decoded.Action != protocol.TaskActionResponsesCreate {
		t.Errorf("expected action=responses.create, got %s", decoded.Action)
	}
}

func TestErrorShape(t *testing.T) {
	errShape := protocol.ErrorShape{
		Code:    "WORKER_ERROR",
		Message: "something went wrong",
	}

	data, err := json.Marshal(errShape)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded protocol.ErrorShape
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Code != "WORKER_ERROR" {
		t.Errorf("expected code=WORKER_ERROR, got %s", decoded.Code)
	}
}

func TestArtifactDescriptor(t *testing.T) {
	desc := protocol.ArtifactDescriptor{
		ArtifactID: "art-123",
		Kind:       protocol.ArtifactKindImage,
		Filename:    "screenshot.png",
		MimeType:   "image/png",
		SizeBytes:  1024,
		SHA256:     "abc123",
		Transport:  protocol.ArtifactTransportInline,
	}

	data, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded protocol.ArtifactDescriptor
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Kind != protocol.ArtifactKindImage {
		t.Errorf("expected kind=image, got %s", decoded.Kind)
	}
}

func TestNewHubClient(t *testing.T) {
	opts := HubClientOptions{
		URL:      "ws://localhost:31880/ws",
		WorkerID: "worker-1",
		Hostname: "test-host",
		Version:  "1.0.0",
	}

	client := New(opts)

	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.opts.WorkerID != "worker-1" {
		t.Errorf("expected WorkerID=worker-1, got %s", client.opts.WorkerID)
	}
}

func TestParseHubMessage(t *testing.T) {
	// Test parsing various frame types
	tests := []struct {
		name     string
		data     string
		expectOK bool
	}{
		{
			name:     "valid req frame",
			data:     `{"type":"req","id":"123","method":"connect","params":{}}`,
			expectOK: true,
		},
		{
			name:     "valid res frame",
			data:     `{"type":"res","id":"123","ok":true}`,
			expectOK: true,
		},
		{
			name:     "valid event frame",
			data:     `{"type":"event","event":"task.assigned","payload":{}}`,
			expectOK: true,
		},
		{
			name:     "invalid json",
			data:     `{invalid}`,
			expectOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var frame HubFrame
			err := json.Unmarshal([]byte(tt.data), &frame)
			if tt.expectOK && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tt.expectOK && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestPendingMap(t *testing.T) {
	pending := make(map[string]chan *protocol.HubResponseFrame)
	ch := make(chan *protocol.HubResponseFrame, 1)

	pending["test-id"] = ch

	if _, ok := pending["test-id"]; !ok {
		t.Error("expected test-id to be in pending map")
	}

	delete(pending, "test-id")

	if _, ok := pending["test-id"]; ok {
		t.Error("expected test-id to be removed")
	}
}

func TestConcurrentPendingAccess(t *testing.T) {
	pending := make(map[string]chan *protocol.HubResponseFrame)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ch := make(chan *protocol.HubResponseFrame, 1)
			pending[string(rune(i))] = ch
			delete(pending, string(rune(i)))
		}(i)
	}

	wg.Wait()
}

func TestMockServer(t *testing.T) {
	var reqCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Just verify the mock server works
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if reqCount != 1 {
		t.Errorf("expected 1 request, got %d", reqCount)
	}
}
