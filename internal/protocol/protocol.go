package protocol

import (
	"time"
)

// Frame types
type FrameType string

const (
	FrameTypeReq   FrameType = "req"
	FrameTypeRes   FrameType = "res"
	FrameTypeEvent FrameType = "event"
)

// TaskAction represents the action type for a task
type TaskAction string

const (
	TaskActionAgent             TaskAction = "agent"
	TaskActionSessionHistoryGet TaskAction = "session.history.get"
	TaskActionNodeInvoke        TaskAction = "node.invoke"
)

// ArtifactKind represents the kind of an artifact
type ArtifactKind string

const (
	ArtifactKindImage  ArtifactKind = "image"
	ArtifactKindVideo  ArtifactKind = "video"
	ArtifactKindAudio  ArtifactKind = "audio"
	ArtifactKindFile   ArtifactKind = "file"
	ArtifactKindJSON   ArtifactKind = "json"
)

// ArtifactTransport represents the transport method for an artifact
type ArtifactTransport string

const (
	ArtifactTransportInline     ArtifactTransport = "inline"
	ArtifactTransportHubFile   ArtifactTransport = "hub_file"
	ArtifactTransportObjectStore ArtifactTransport = "object_store"
)

// ErrorShape represents an error structure
type ErrorShape struct {
	Code      string      `json:"code"`
	Message   string      `json:"message"`
	Details   interface{} `json:"details,omitempty"`
	Retryable *bool       `json:"retryable,omitempty"`
}

// HubRequestFrame represents a Hub request frame
type HubRequestFrame struct {
	Type   FrameType     `json:"type"`
	ID     string        `json:"id"`
	Method string        `json:"method"`
	Params interface{}   `json:"params,omitempty"`
}

// HubResponseFrame represents a Hub response frame
type HubResponseFrame struct {
	Type    FrameType     `json:"type"`
	ID      string        `json:"id"`
	OK      bool          `json:"ok"`
	Payload interface{}   `json:"payload,omitempty"`
	Error   *ErrorShape  `json:"error,omitempty"`
}

// HubEventFrame represents a Hub event frame
type HubEventFrame struct {
	Type    FrameType   `json:"type"`
	Event   string      `json:"event"`
	Payload interface{} `json:"payload,omitempty"`
	Seq     *int        `json:"seq,omitempty"`
}

// HubClientInfo represents client information in connect params
type HubClientInfo struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	Platform   string `json:"platform"`
	Mode       string `json:"mode"`
	DisplayName string `json:"displayName,omitempty"`
	InstanceID string `json:"instanceId,omitempty"`
}

// WorkerRegistration represents worker registration info
type WorkerRegistration struct {
	WorkerID    string   `json:"worker_id"`
	TenantID    string   `json:"tenant_id,omitempty"`
	Env        string   `json:"env,omitempty"`
	Hostname    string   `json:"hostname"`
	Version     string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// AdapterRegistration represents adapter registration info
type AdapterRegistration struct {
	AdapterID string `json:"adapter_id"`
	TenantID  string `json:"tenant_id,omitempty"`
	Env       string `json:"env,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Version   string `json:"version"`
}

// HubConnectParams represents parameters for Hub connection
type HubConnectParams struct {
	MinProtocol int                    `json:"minProtocol"`
	MaxProtocol int                    `json:"maxProtocol"`
	Client      HubClientInfo          `json:"client"`
	Role        string                 `json:"role"`
	Auth        *HubAuth               `json:"auth,omitempty"`
	Nonce       string                 `json:"nonce"`
	Worker      *WorkerRegistration    `json:"worker,omitempty"`
	Adapter     *AdapterRegistration   `json:"adapter,omitempty"`
}

// HubAuth represents authentication info
type HubAuth struct {
	Token *string `json:"token,omitempty"`
}

// HubHelloOk represents the hello-ok response from Hub
type HubHelloOk struct {
	Type     string              `json:"type"`
	Protocol int                 `json:"protocol"`
	Server   HubServerInfo        `json:"server"`
	Policy   HubPolicy            `json:"policy"`
	Features HubFeatures          `json:"features"`
}

// HubServerInfo represents server info in hello-ok
type HubServerInfo struct {
	Version string `json:"version"`
	ConnID  string `json:"connId"`
}

// HubPolicy represents policy info in hello-ok
type HubPolicy struct {
	HeartbeatIntervalMs    int `json:"heartbeatIntervalMs"`
	MaxInlineArtifactBytes int `json:"maxInlineArtifactBytes"`
}

// HubFeatures represents features info in hello-ok
type HubFeatures struct {
	Methods []string `json:"methods"`
	Events  []string `json:"events"`
}

// TaskAssignedPayload represents the payload for task.assigned event
type TaskAssignedPayload struct {
	RequestID           string      `json:"request_id"`
	SessionID          string      `json:"session_id"`
	WorkerID           string      `json:"worker_id"`
	AdapterID          string      `json:"adapter_id"`
	OpenclawSessionKey string      `json:"openclaw_session_key"`
	Action             TaskAction  `json:"action"`
	Input              interface{} `json:"input"`
	CreatedAt          time.Time   `json:"created_at"`
}

// TaskAcceptedPayload represents the payload for task.accepted request
type TaskAcceptedPayload struct {
	RequestID   string    `json:"request_id"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

// TaskOutputPayload represents the payload for task.output request
type TaskOutputPayload struct {
	RequestID   string      `json:"request_id"`
	OutputIndex int         `json:"output_index"`
	Output      interface{} `json:"output"`
	EmittedAt   time.Time   `json:"emitted_at"`
}

// ArtifactDescriptor represents an artifact descriptor
type ArtifactDescriptor struct {
	ArtifactID      string            `json:"artifact_id"`
	Kind            ArtifactKind      `json:"kind"`
	Filename        string            `json:"filename"`
	MimeType        string            `json:"mime_type"`
	SizeBytes       int               `json:"size_bytes"`
	SHA256          string            `json:"sha256"`
	Transport       ArtifactTransport `json:"transport"`
	InlineBase64    *string           `json:"inline_base64,omitempty"`
	DownloadURL     *string           `json:"download_url,omitempty"`
	ExpiresAt       *time.Time       `json:"expires_at,omitempty"`
	Meta            map[string]interface{} `json:"meta,omitempty"`
}

// TaskCompletedPayload represents the payload for task.completed request
type TaskCompletedPayload struct {
	RequestID   string              `json:"request_id"`
	CompletedAt time.Time           `json:"completed_at"`
	Result      interface{}         `json:"result"`
	Artifacts   []ArtifactDescriptor `json:"artifacts"`
}

// TaskFailedPayload represents the payload for task.failed request
type TaskFailedPayload struct {
	RequestID string      `json:"request_id"`
	FailedAt  time.Time   `json:"failed_at"`
	Error     ErrorShape  `json:"error"`
}

// WorkerHeartbeatPayload represents the payload for worker.heartbeat request
type WorkerHeartbeatPayload struct {
	WorkerID string    `json:"worker_id"`
	TS       time.Time `json:"ts"`
}

// ArtifactRegisterParams represents parameters for artifact.register request
type ArtifactRegisterParams struct {
	RequestID string           `json:"request_id"`
	Artifact  ArtifactInput    `json:"artifact"`
}

// ArtifactInput represents an artifact input for registration
type ArtifactInput struct {
	Kind         ArtifactKind               `json:"kind"`
	Filename     string                     `json:"filename"`
	MimeType     string                     `json:"mime_type"`
	ContentBase64 string                    `json:"content_base64"`
	Meta         map[string]interface{}     `json:"meta,omitempty"`
}

// NormalizedArtifact represents a normalized artifact from node.invoke
type NormalizedArtifact struct {
	Kind         string
	Filename     string
	MimeType     string
	ContentBase64 string
	Meta         map[string]interface{}
}
