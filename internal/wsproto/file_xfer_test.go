package wsproto

import "testing"

// TestFileXferRoundTripAndSupports pins the XFER-01 wire contract: both new frames
// survive EncodeFrame → DecodeEnvelope → As unchanged (so the two sides cannot drift
// on a json tag), the capability floor is v9 (a v8 peer must NOT be offered the frame —
// the hub refuses the transfer instead, G032), and this build still reports v9.
func TestFileXferRoundTripAndSupports(t *testing.T) {
	if CurrentProtocolVersion != 9 {
		t.Fatalf("CurrentProtocolVersion = %d, want 9 (the version that carries file_xfer)", CurrentProtocolVersion)
	}
	if FileXferMinProtocolVersion != 9 {
		t.Fatalf("FileXferMinProtocolVersion = %d, want 9", FileXferMinProtocolVersion)
	}
	if SupportsFileXfer(8) {
		t.Fatal("SupportsFileXfer(8) = true, want false: a v8 worker has no file_xfer frame")
	}
	if !SupportsFileXfer(9) || !SupportsFileXfer(10) {
		t.Fatal("SupportsFileXfer(9/10) = false, want true")
	}

	req := FileXfer{
		XferID:     "xf-1",
		Op:         "put",
		ProjectKey: "alpha",
		Path:       "tmp/in/firmware.bin",
		Size:       5 << 20,
		SHA256:     "ab12",
		Force:      true,
		URLPath:    "/v1/xfer/xf-1/content",
	}
	raw, err := EncodeFrame(TypeFileXfer, "", req)
	if err != nil {
		t.Fatalf("encode file_xfer: %v", err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode file_xfer envelope: %v", err)
	}
	if env.Type != TypeFileXfer {
		t.Fatalf("decoded type = %q, want %q", env.Type, TypeFileXfer)
	}
	if env.JobID != "" {
		t.Fatalf("file_xfer job_id = %q, want empty: a transfer is not a job", env.JobID)
	}
	got, err := As[FileXfer](env)
	if err != nil {
		t.Fatalf("decode file_xfer payload: %v", err)
	}
	if got != req {
		t.Fatalf("file_xfer round trip = %+v, want %+v", got, req)
	}

	res := FileXferResult{XferID: "xf-1", OK: false, Size: 12, SHA256: "cd34", Error: "exists", DurationMS: 42}
	raw, err = EncodeFrame(TypeFileXferResult, "", res)
	if err != nil {
		t.Fatalf("encode file_xfer_result: %v", err)
	}
	env, err = DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode file_xfer_result envelope: %v", err)
	}
	if env.Type != TypeFileXferResult {
		t.Fatalf("decoded type = %q, want %q", env.Type, TypeFileXferResult)
	}
	gotRes, err := As[FileXferResult](env)
	if err != nil {
		t.Fatalf("decode file_xfer_result payload: %v", err)
	}
	if gotRes != res {
		t.Fatalf("file_xfer_result round trip = %+v, want %+v", gotRes, res)
	}
}
