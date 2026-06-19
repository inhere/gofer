package wsproto

import (
	"encoding/json"
	"testing"
)

// TestEncodeDecodeRoundTrip exercises every P1 frame: EncodeFrame → DecodeEnvelope
// → As must restore the original payload identically and preserve type + job_id.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Run("register", func(t *testing.T) {
		in := Register{WorkerID: "w1", Labels: []string{"gpu"}, Projects: []string{"p"}, Agents: []string{"exec"}, MaxConcurrent: 4}
		b, err := EncodeFrame(TypeRegister, "", in)
		if err != nil {
			t.Fatal(err)
		}
		env, err := DecodeEnvelope(b)
		if err != nil {
			t.Fatal(err)
		}
		if env.Type != TypeRegister || env.JobID != "" {
			t.Fatalf("bad envelope: %+v", env)
		}
		out, err := As[Register](env)
		if err != nil {
			t.Fatal(err)
		}
		if out.WorkerID != "w1" || out.MaxConcurrent != 4 || len(out.Labels) != 1 || out.Labels[0] != "gpu" {
			t.Fatalf("register round-trip mismatch: %+v", out)
		}
	})

	t.Run("registered", func(t *testing.T) {
		in := Registered{Accepted: true, ServerTime: 1745989200000}
		b, _ := EncodeFrame(TypeRegistered, "", in)
		env, _ := DecodeEnvelope(b)
		out, _ := As[Registered](env)
		if !out.Accepted || out.ServerTime != 1745989200000 {
			t.Fatalf("registered round-trip mismatch: %+v", out)
		}
	})

	t.Run("dispatch", func(t *testing.T) {
		in := Dispatch{JobID: "j1", ProjectKey: "p", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"}, TimeoutSec: 30}
		b, _ := EncodeFrame(TypeDispatch, in.JobID, in)
		env, _ := DecodeEnvelope(b)
		if env.JobID != "j1" {
			t.Fatalf("dispatch job_id lost: %q", env.JobID)
		}
		out, _ := As[Dispatch](env)
		if out.Runner != "local" || len(out.Cmd) != 2 || out.Cmd[1] != "hi" || out.TimeoutSec != 30 {
			t.Fatalf("dispatch round-trip mismatch: %+v", out)
		}
	})

	t.Run("log", func(t *testing.T) {
		in := Log{JobID: "j1", Stream: "stdout", Seq: 7, Text: "hello\nworld"}
		b, _ := EncodeFrame(TypeLog, in.JobID, in)
		env, _ := DecodeEnvelope(b)
		out, _ := As[Log](env)
		if out.Seq != 7 || out.Stream != "stdout" || out.Text != "hello\nworld" {
			t.Fatalf("log round-trip mismatch: %+v", out)
		}
	})

	t.Run("status", func(t *testing.T) {
		in := Status{JobID: "j1", Status: "running"}
		b, _ := EncodeFrame(TypeStatus, in.JobID, in)
		env, _ := DecodeEnvelope(b)
		out, _ := As[Status](env)
		if out.Status != "running" {
			t.Fatalf("status round-trip mismatch: %+v", out)
		}
	})

	t.Run("result", func(t *testing.T) {
		in := Result{JobID: "j1", Status: "done", ExitCode: 0}
		b, _ := EncodeFrame(TypeResult, in.JobID, in)
		env, _ := DecodeEnvelope(b)
		out, _ := As[Result](env)
		if out.Status != "done" || out.ExitCode != 0 {
			t.Fatalf("result round-trip mismatch: %+v", out)
		}
	})
}

// TestDecodeUnknownType proves an unknown frame type does not error and is not a
// panic: the envelope decodes with the raw type preserved (forward compat).
func TestDecodeUnknownType(t *testing.T) {
	raw := []byte(`{"type":"future-frame","job_id":"j9","payload":{"x":1}}`)
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("unknown type should decode cleanly: %v", err)
	}
	if env.Type != "future-frame" || env.JobID != "j9" {
		t.Fatalf("unknown envelope fields wrong: %+v", env)
	}
}

// TestServerTimeMillis pins Registered.ServerTime to milliseconds (SR102).
func TestServerTimeMillis(t *testing.T) {
	b, _ := EncodeFrame(TypeRegistered, "", Registered{Accepted: true, ServerTime: 1745989200123})
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	out, _ := As[Registered](env)
	// 13-digit value is millisecond precision; a seconds value would be 10 digits.
	if out.ServerTime < 1_000_000_000_000 {
		t.Fatalf("server_time not in milliseconds: %d", out.ServerTime)
	}
}

// TestAsEmptyPayload proves a body-less envelope yields the zero value (no error).
func TestAsEmptyPayload(t *testing.T) {
	env := Envelope{Type: TypePing}
	out, err := As[Ping](env)
	if err != nil {
		t.Fatal(err)
	}
	if out.TS != 0 {
		t.Fatalf("expected zero Ping, got %+v", out)
	}
}
