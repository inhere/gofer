package wsproto

import (
	"encoding/json"
	"testing"
)

// TestDispatchRoundTripsUploadsCollect: the job-side file-transfer fields ride
// Dispatch additively (XFER-01 X2, protocol v9) — the hub resolved the staged upload
// ids + their destinations and the collect globs, and the worker that executes the
// job must receive them verbatim (it materializes the uploads in ITS cwd and matches
// the globs there).
func TestDispatchRoundTripsUploadsCollect(t *testing.T) {
	d := Dispatch{
		JobID: "j1", ProjectKey: "p", Agent: "exec", Runner: "local",
		Uploads: []XferUpload{{XferID: "xf-1", Dest: "tmp/in/a.bin"}, {XferID: "xf-2", Dest: "b.txt"}},
		Collect: []string{"tmp/out/*.csv", "*.log"},
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	var back Dispatch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal dispatch: %v", err)
	}
	if len(back.Uploads) != 2 || back.Uploads[0].XferID != "xf-1" || back.Uploads[0].Dest != "tmp/in/a.bin" {
		t.Fatalf("uploads round trip = %+v, want the staged ids + destinations", back.Uploads)
	}
	if len(back.Collect) != 2 || back.Collect[0] != "tmp/out/*.csv" || back.Collect[1] != "*.log" {
		t.Fatalf("collect round trip = %v, want both globs", back.Collect)
	}

	// Additive: a hub that predates the fields (neither key) still decodes.
	var old Dispatch
	if err := json.Unmarshal([]byte(`{"job_id":"j2","project_key":"p","agent":"exec","runner":"local"}`), &old); err != nil {
		t.Fatalf("a dispatch without the xfer fields must still decode: %v", err)
	}
	if len(old.Uploads) != 0 || len(old.Collect) != 0 {
		t.Fatalf("absent fields decoded to %+v/%v, want empty", old.Uploads, old.Collect)
	}

	// A plain dispatch stays byte-identical to before (no empty keys on the wire).
	plain, err := json.Marshal(Dispatch{JobID: "j3"})
	if err != nil {
		t.Fatalf("marshal plain dispatch: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		t.Fatalf("unmarshal plain dispatch: %v", err)
	}
	for _, k := range []string{"uploads", "collect"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("a plain dispatch must not carry %q: %s", k, plain)
		}
	}
}

// TestSupportsFileXferCoversJobXfer: v9 is where BOTH the transfer frames and the
// job-side uploads/collect dispatch fields enter the protocol, so the hub's
// capability gate for a job that carries files is the same floor — one version,
// one capability, no way for the two to drift apart.
func TestSupportsFileXferCoversJobXfer(t *testing.T) {
	if SupportsFileXfer(FileXferMinProtocolVersion - 1) {
		t.Fatalf("a v%d worker must not be told it can carry job files", FileXferMinProtocolVersion-1)
	}
	if !SupportsFileXfer(FileXferMinProtocolVersion) {
		t.Fatalf("a v%d worker must be able to carry job files", FileXferMinProtocolVersion)
	}
	if CurrentProtocolVersion < FileXferMinProtocolVersion {
		t.Fatalf("the implemented protocol (%d) is below the file-transfer floor (%d)", CurrentProtocolVersion, FileXferMinProtocolVersion)
	}
}
