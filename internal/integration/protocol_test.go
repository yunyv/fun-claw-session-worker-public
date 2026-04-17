package integration

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/funclaw/go-worker/internal/protocol"
)

func TestHubProtocolCompliance(t *testing.T) {
	t.Run("task.assigned payload matches TypeScript schema", func(t *testing.T) {
		payload := protocol.TaskAssignedPayload{
			RequestID:           "req-123",
			SessionID:          "session-1",
			WorkerID:           "worker-1",
			AdapterID:          "adapter-1",
			OpenclawSessionKey: "openclaw:session-1",
			Action:             protocol.TaskActionResponsesCreate,
			Input:              map[string]string{"model": "openclaw", "input": "hi"},
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
	})

	t.Run("task.completed with artifacts", func(t *testing.T) {
		inlineBase64 := "SGVsbG8gV29ybGQ="
		artifact := protocol.ArtifactDescriptor{
			ArtifactID:   "art-123",
			Kind:         protocol.ArtifactKindImage,
			Filename:     "test.png",
			MimeType:     "image/png",
			SizeBytes:    11,
			SHA256:       "abc123",
			Transport:    protocol.ArtifactTransportInline,
			InlineBase64: &inlineBase64,
		}

		payload := protocol.TaskCompletedPayload{
			RequestID:   "req-123",
			CompletedAt: time.Now(),
			Result:      map[string]string{"output_text": "hello"},
			Artifacts:   []protocol.ArtifactDescriptor{artifact},
		}

		data, _ := json.Marshal(payload)

		var parsed map[string]interface{}
		json.Unmarshal(data, &parsed)

		artifacts := parsed["artifacts"].([]interface{})
		if len(artifacts) != 1 {
			t.Errorf("expected 1 artifact, got %d", len(artifacts))
		}
	})
}

func TestConcurrentMessageHandling(t *testing.T) {
	var mu sync.Mutex
	messages := make([]string, 0)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mu.Lock()
			messages = append(messages, string(rune('0'+id)))
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(messages) != 10 {
		t.Errorf("expected 10 messages, got %d", len(messages))
	}
}

func TestHubProtocolFrameTypes(t *testing.T) {
	tests := []struct {
		name     string
		frame    interface{}
		expected protocol.FrameType
	}{
		{
			name:     "req frame",
			frame:    protocol.HubRequestFrame{Type: protocol.FrameTypeReq, ID: "1", Method: "connect"},
			expected: protocol.FrameTypeReq,
		},
		{
			name:     "res frame",
			frame:    protocol.HubResponseFrame{Type: protocol.FrameTypeRes, ID: "1", OK: true},
			expected: protocol.FrameTypeRes,
		},
		{
			name:     "event frame",
			frame:    protocol.HubEventFrame{Type: protocol.FrameTypeEvent, Event: "task.assigned"},
			expected: protocol.FrameTypeEvent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.frame)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var parsed map[string]interface{}
			json.Unmarshal(data, &parsed)

			if parsed["type"] != string(tt.expected) {
				t.Errorf("expected type=%s, got %v", tt.expected, parsed["type"])
			}
		})
	}
}
