// Package wsproto defines the WebSocket message protocol shared by the hub
// (internal/wshub, serve-side) and the worker client (internal/worker). It is a
// pure leaf package: it has NO dependency on job/runner/config so both sides can
// import it without cycles (main plan §4, §5).
//
// Wire model: a single multiplexed connection carries every job's frames. Each
// message is an Envelope discriminated by Type and demuxed by JobID on the hub.
// The full P1..P3 frame set is declared up front (main plan §5 / review #6: no
// breaking protocol churn later); P2/P3 frame BEHAVIOUR is not implemented in
// WP1, only the types exist.
package wsproto

import "encoding/json"

// FrameType is the envelope discriminator (main plan §5 frame table).
type FrameType string

const (
	// P1 frames (端到端远程执行).
	TypeRegister   FrameType = "register"   // w→s
	TypeRegistered FrameType = "registered" // s→w
	TypeDispatch   FrameType = "dispatch"   // s→w
	TypeLog        FrameType = "log"        // w→s
	TypeStatus     FrameType = "status"     // w→s
	TypeResult     FrameType = "result"     // w→s
	// TypeOutcome (w→s, P4 产出与审计回传): sent just before the terminal result so
	// the host applies the worker-captured产出 before finishing the job. Optional —
	// an old worker never sends it (the read loop ignores an unknown opcode anyway).
	TypeOutcome FrameType = "outcome" // w→s

	// P2 frames (declared as placeholders; behaviour implemented in P2).
	TypeCancel      FrameType = "cancel"      // s→w
	TypeInteraction FrameType = "interaction" // w→s
	TypeAnswer      FrameType = "answer"      // s→w

	// P3 frames (heartbeat; declared as placeholders).
	TypePing FrameType = "ping" // both
	TypePong FrameType = "pong" // both

	// Config hot-reload frames (protocol v3, see ReloadMinProtocolVersion).
	// reload/reload_result form an RPC PAIR correlated by request_id; caps is an
	// UNSOLICITED broadcast. They are deliberately distinct types: a broadcast must
	// never be able to pose as the response to a pending reload (see the Caps doc).
	TypeReload       FrameType = "reload"        // s→w
	TypeReloadResult FrameType = "reload_result" // w→s
	TypeCaps         FrameType = "caps"          // w→s

	// Policy push frames (protocol v4, see PolicyMinProtocolVersion). policy is the
	// server→worker authoritative project/guard set; applied is the worker→server
	// report of what it converged to. A policy may also ride on the Registered ack
	// (catch-up on register); the standalone frame carries later revisions.
	TypePolicy  FrameType = "policy"  // s→w
	TypeApplied FrameType = "applied" // w→s
)

// Envelope is the single-connection multiplexed message. Payload carries the
// type-specific body as raw JSON so the reader can demux on Type/JobID before
// decoding the body (double-stage decode keeps wsproto free of job/runner).
type Envelope struct {
	Type    FrameType       `json:"type"`
	JobID   string          `json:"job_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EncodeFrame marshals a typed payload into an Envelope's JSON bytes. jobID is
// the demux key (may be empty for register/registered which are connection-level
// rather than per-job).
func EncodeFrame(t FrameType, jobID string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{Type: t, JobID: jobID, Payload: raw})
}

// DecodeEnvelope unmarshals raw bytes into an Envelope (type + job_id + the raw
// payload). An unknown Type is NOT an error: the envelope decodes and the reader
// may ignore it (forward compatibility, review #6).
func DecodeEnvelope(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// As decodes env.Payload into the typed payload T. A nil/empty payload yields
// the zero value of T (no error), so a body-less frame still round-trips.
func As[T any](env Envelope) (T, error) {
	var v T
	if len(env.Payload) == 0 {
		return v, nil
	}
	err := json.Unmarshal(env.Payload, &v)
	return v, err
}
