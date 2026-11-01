package wsproto

// PreviewMinProtocolVersion is the first protocol version that carries the job-preview
// frames (preview_open and the dedicated preview-connect hello). It does NOT raise
// MinProtocolVersion: a worker below it registers and runs jobs exactly as before, it just
// cannot serve a preview, so the server refuses to create a lease for a job on it (409,
// "worker needs an upgrade") instead of silently ignoring a frame the peer cannot read.
const PreviewMinProtocolVersion = 29

// SupportsPreview reports whether a peer that registered with protocol version proto
// serves job previews. It is the single place that knows which version gained the
// capability.
func SupportsPreview(proto int) bool { return proto >= PreviewMinProtocolVersion }

// PreviewConnectPath is the worker-authenticated endpoint a worker dials back to carry
// the bytes of ONE preview connection (a WebSocket whose binary messages are the TCP
// stream). It is deliberately not the generic tunnel endpoint: a preview connection is
// bound to a lease, a job and a worker instance, and has no allowlist.
const PreviewConnectPath = "/v1/workers/preview-connect"

// TypePreviewOpen (s→w, protocol v29) asks a worker to open ONE data connection to a
// job's preview port and relay its bytes back through PreviewConnectPath.
const TypePreviewOpen FrameType = "preview_open"

// PreviewOpen is the preview_open payload. The worker accepts it only when JobID is a job
// it is still running AND InstanceID is this process, always dials 127.0.0.1:Port (never a
// host from the frame), and answers on PreviewConnectPath with a PreviewHello carrying
// ConnID and RelayNonce. Each ConnID / RelayNonce is single-use.
type PreviewOpen struct {
	PreviewID  string `json:"preview_id"`
	JobID      string `json:"job_id"`
	Port       int    `json:"port"`
	RelayNonce string `json:"relay_nonce"`
	// ConnID names this one connection (the rendezvous the hello answers).
	ConnID string `json:"conn_id"`
	// InstanceID is the worker process instance the lease was created against.
	InstanceID string `json:"instance_id"`
}

// PreviewHello is the first message of a PreviewConnectPath WebSocket. A non-empty
// ErrorCode means the worker refused or failed to dial and no byte stream follows.
type PreviewHello struct {
	PreviewID  string `json:"preview_id"`
	JobID      string `json:"job_id"`
	ConnID     string `json:"conn_id"`
	RelayNonce string `json:"relay_nonce"`
	ErrorCode  string `json:"error_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Preview refusal codes a worker reports in PreviewHello.ErrorCode.
const (
	PreviewCodeJobNotRunning  = "job_not_running"
	PreviewCodeInstance       = "instance_mismatch"
	PreviewCodeBadPort        = "bad_port"
	PreviewCodeNonceReplay    = "nonce_replay"
	PreviewCodeDialFailed     = "dial_failed"
	PreviewCodeTooManyConnect = "too_many_connections"
)
