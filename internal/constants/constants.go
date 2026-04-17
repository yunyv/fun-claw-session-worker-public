package constants

const (
	// HubProtocolVersion is the protocol version used for Hub communication
	HubProtocolVersion = 1

	// HubDefaultPort is the default port for Hub
	HubDefaultPort = 31880

	// HubDefaultHost is the default host for Hub
	HubDefaultHost = "0.0.0.0"

	// HubDefaultDataDirName is the default data directory name for Hub
	HubDefaultDataDirName = "funclaw-hub"

	// WorkerDefaultStateDirName is the default state directory name for Worker
	WorkerDefaultStateDirName = "funclaw-worker"

	// HubDefaultHeartbeatIntervalMs is the default heartbeat interval in milliseconds
	HubDefaultHeartbeatIntervalMs = 15000

	// HubDefaultConnectTimeoutMs is the default connection timeout in milliseconds
	HubDefaultConnectTimeoutMs = 10000

	// HubDefaultRequestTimeoutMs is the default request timeout in milliseconds
	HubDefaultRequestTimeoutMs = 30000

	// HubDefaultMaxBodyBytes is the default maximum body size
	HubDefaultMaxBodyBytes = 5000000

	// MaxInlineArtifactBytes is the maximum size for inline artifacts
	MaxInlineArtifactBytes = 1048576
)
