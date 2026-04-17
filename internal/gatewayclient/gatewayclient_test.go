package gatewayclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetSessionHistory_SendsOperatorReadScope(t *testing.T) {
	var seenAuth, seenScopes string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenScopes = r.Header.Get("X-OpenClaw-Scopes")
		// path seen but not used
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"history": []interface{}{}})
	}))
	defer server.Close()

	gw := New(server.URL, "gateway-secret", server.URL+"/ws")

	result, err := gw.GetSessionHistory(
		context.Background(),
		"session-1",
		map[string]interface{}{"limit": 20, "cursor": "abc"},
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if _, ok := resultMap["history"]; !ok {
		t.Errorf("expected history field in result")
	}
	if seenAuth != "Bearer gateway-secret" {
		t.Errorf("expected auth=Bearer gateway-secret, got %s", seenAuth)
	}
	if seenScopes != "operator.read" {
		t.Errorf("expected scopes=operator.read, got %s", seenScopes)
	}
}

func TestNormalizeNodeArtifacts(t *testing.T) {
	gw := New("http://localhost:18789", "", "ws://localhost:18789")

	tests := []struct {
		name    string
		input   interface{}
		wantArt int
	}{
		{"nil input", nil, 0},
		{"non-map input", "string", 0},
		{"no base64", map[string]interface{}{"result": "some text"}, 0},
		{"with base64 image", map[string]interface{}{
			"base64":   "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
			"format":   "png",
			"mimeType": "image/png",
		}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, arts := gw.NormalizeNodeArtifacts(tt.input)
			if len(arts) != tt.wantArt {
				t.Errorf("expected %d artifacts, got %d", tt.wantArt, len(arts))
			}
		})
	}
}

func TestDetectArtifactKind(t *testing.T) {
	tests := []struct {
		mimeType string
		expected string
	}{
		{"image/png", "image"},
		{"image/jpeg", "image"},
		{"video/mp4", "video"},
		{"audio/mp3", "audio"},
		{"application/pdf", "file"},
		{"", "file"},
	}
	for _, tt := range tests {
		got := detectArtifactKind(tt.mimeType)
		if got != tt.expected {
			t.Errorf("detectArtifactKind(%q) = %q, want %q", tt.mimeType, got, tt.expected)
		}
	}
}

func TestMimeTypeFromFormat(t *testing.T) {
	tests := []struct {
		format   string
		expected string
	}{
		{"jpg", "image/jpeg"},
		{"jpeg", "image/jpeg"},
		{"png", "image/png"},
		{"mp4", "video/mp4"},
		{"unknown", "application/octet-stream"},
	}
	for _, tt := range tests {
		got := mimeTypeFromFormat(tt.format)
		if got != tt.expected {
			t.Errorf("mimeTypeFromFormat(%q) = %q, want %q", tt.format, got, tt.expected)
		}
	}
}

func TestEncodeURIComponent(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"session-1", "session-1"},
		{"foo:bar", "foo%3Abar"},
		{"a b", "a%20b"},
		{"hello", "hello"},
	}
	for _, tt := range tests {
		got := encodeURIComponent(tt.input)
		if got != tt.expected {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestGetSessionHistory_ErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error": "not found"}`)
	}))
	defer server.Close()

	gw := New(server.URL, "token", server.URL)

	_, err := gw.GetSessionHistory(context.Background(), "session-1", nil)

	if err == nil {
		t.Error("expected error for non-200 response")
	}
}
