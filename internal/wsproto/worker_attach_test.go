package wsproto

import "testing"

// worker_attach_test.go pins the v27 capability: the frame carries an optional target that
// moves a put into the executing WORKER's own attachment directory, and only a peer that
// declared the floor may be sent one.

func TestWorkerAttachProtocolFloor(t *testing.T) {
	if WorkerAttachMinProtocolVersion != 27 {
		t.Fatalf("WorkerAttachMinProtocolVersion = %d, want 27", WorkerAttachMinProtocolVersion)
	}
	if !SupportsWorkerAttach(CurrentProtocolVersion) {
		t.Fatalf("SupportsWorkerAttach(%d) = false: raising the protocol must not drop a floor", CurrentProtocolVersion)
	}
	if SupportsWorkerAttach(WorkerAttachMinProtocolVersion - 1) {
		t.Fatalf("SupportsWorkerAttach(%d) = true: a v26 worker drops the target key", WorkerAttachMinProtocolVersion-1)
	}
	// The attachment target rides the SAME frame the v9 floor introduced: an old worker has
	// the frame but not the field, so the two floors must stay independent.
	if FileXferMinProtocolVersion != 9 || !SupportsFileXfer(9) {
		t.Fatalf("FileXferMinProtocolVersion = %d, want the unchanged v9 floor", FileXferMinProtocolVersion)
	}
	if MinProtocolVersion > WorkerAttachMinProtocolVersion {
		t.Fatalf("MinProtocolVersion = %d must stay below the attachment floor", MinProtocolVersion)
	}
}

func TestFileXferTargetRoundTrips(t *testing.T) {
	want := FileXfer{
		XferID: "xf-abc12345",
		Op:     "put",
		Target: FileXferTargetAttachments,
		Path:   "20261027/xf-abc12345.png",
		Size:   12,
		SHA256: "ab12",
		Force:  true,
	}
	raw, err := EncodeFrame(TypeFileXfer, "", want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	fr, err := As[FileXfer](env)
	if err != nil {
		t.Fatalf("As[FileXfer]: %v", err)
	}
	if fr.Target != FileXferTargetAttachments || fr.ProjectKey != "" || fr.Path != want.Path {
		t.Fatalf("decoded = %+v, want the attachment target with no project", fr)
	}
	// A project transfer keeps the field empty on the wire (omitempty), so a v26 peer and a
	// pre-v27 row decode exactly as before.
	plain, err := EncodeFrame(TypeFileXfer, "", FileXfer{XferID: "xf-1", Op: "put", ProjectKey: "alpha", Path: "tmp/x"})
	if err != nil {
		t.Fatalf("EncodeFrame(project): %v", err)
	}
	plainEnv, err := DecodeEnvelope(plain)
	if err != nil {
		t.Fatalf("decode envelope(project): %v", err)
	}
	got, err := As[FileXfer](plainEnv)
	if err != nil {
		t.Fatalf("As[FileXfer](project): %v", err)
	}
	if got.Target != "" || got.ProjectKey != "alpha" {
		t.Fatalf("decoded project put = %+v, want an empty target", got)
	}
}
