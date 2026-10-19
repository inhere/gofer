package wsproto

import (
	"encoding/json"
	"testing"
)

// TestXferAbsPathProtocolFloor pins the v25 addition: the absolute destination a worker
// reports for a put is an OPTIONAL field, so the capability is negotiated per peer
// (SupportsXferAbsPath) and nothing is refused over it — a v24 worker keeps transferring,
// it just leaves the field empty (G032).
func TestXferAbsPathProtocolFloor(t *testing.T) {
	if XferAbsPathMinProtocolVersion != 25 {
		t.Fatalf("XferAbsPathMinProtocolVersion = %d, want 25", XferAbsPathMinProtocolVersion)
	}
	if CurrentProtocolVersion < XferAbsPathMinProtocolVersion {
		t.Fatalf("CurrentProtocolVersion = %d is below the abs-path floor %d: this build must report one",
			CurrentProtocolVersion, XferAbsPathMinProtocolVersion)
	}
	if SupportsXferAbsPath(XferAbsPathMinProtocolVersion - 1) {
		t.Fatalf("SupportsXferAbsPath(%d) = true: a v24 worker never sends abs_path",
			XferAbsPathMinProtocolVersion-1)
	}
	if !SupportsXferAbsPath(XferAbsPathMinProtocolVersion) {
		t.Fatalf("SupportsXferAbsPath(%d) = false", XferAbsPathMinProtocolVersion)
	}
	if !SupportsXferAbsPath(CurrentProtocolVersion) {
		t.Fatalf("SupportsXferAbsPath(%d) = false: raising the protocol must not drop a floor", CurrentProtocolVersion)
	}
	// The file-transfer frame floor is untouched by this bump: a v9 worker still transfers.
	if FileXferMinProtocolVersion != 9 || !SupportsFileXfer(9) {
		t.Fatalf("FileXferMinProtocolVersion = %d (SupportsFileXfer(9)=%v), want an unchanged v9 floor",
			FileXferMinProtocolVersion, SupportsFileXfer(9))
	}
}

// TestFileXferResultAbsPathRoundTrip: the reported destination survives the wire
// unchanged, and a frame written by a v24 worker (no abs_path key at all) decodes to the
// empty path the server then treats as "not reported".
func TestFileXferResultAbsPathRoundTrip(t *testing.T) {
	want := FileXferResult{
		XferID:     "xf-abc12345",
		OK:         true,
		Size:       12,
		SHA256:     "cd34",
		DurationMS: 7,
		AbsPath:    "/d/work/inhere/my-tools-dev/tmp/gofer-attachments/20261019/xf-abc12345.png",
	}
	raw, err := EncodeFrame(TypeFileXferResult, "", want)
	if err != nil {
		t.Fatalf("encode file_xfer_result: %v", err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	got, err := As[FileXferResult](env)
	if err != nil {
		t.Fatalf("decode file_xfer_result: %v", err)
	}
	if got != want {
		t.Fatalf("file_xfer_result round trip = %+v, want %+v", got, want)
	}

	// A pre-v25 frame carries no abs_path key: the field must read back empty rather than
	// failing the decode (that empty value IS the old-worker signal).
	var old FileXferResult
	if err := json.Unmarshal([]byte(`{"xfer_id":"xf-old","ok":true,"size":3,"sha256":"ee","duration_ms":1}`), &old); err != nil {
		t.Fatalf("decode a v24 file_xfer_result: %v", err)
	}
	if old.AbsPath != "" {
		t.Fatalf("v24 frame abs_path = %q, want empty", old.AbsPath)
	}
	if !old.OK || old.XferID != "xf-old" {
		t.Fatalf("v24 frame lost its other fields: %+v", old)
	}

	// And an unreported path is omitted from the encoded frame, so a get (or a v24-shaped
	// result) keeps the exact pre-v25 payload shape.
	blank, err := json.Marshal(FileXferResult{XferID: "xf-blank", OK: true})
	if err != nil {
		t.Fatalf("marshal a result without a path: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(blank, &keys); err != nil {
		t.Fatalf("unmarshal the encoded result: %v", err)
	}
	if _, ok := keys["abs_path"]; ok {
		t.Fatalf("an empty abs_path was encoded: %s", blank)
	}
}
