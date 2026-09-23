package worker

import (
	"testing"

	"github.com/inhere/gofer/internal/runner"
)

// TestSplitSkillUploadsDropsBaseOnOldWorker pins the JOB-10 negotiation: a skills
// mount (Base "result_dir") must never reach a worker that cannot see the base field,
// because such a worker drops the key and writes the file into the job's CWD — the
// shared checkout the design forbids. The job is dispatched WITHOUT the mount instead
// (the caller reports dropped > 0 as job.skills_skipped), so the split has to drop
// exactly the base-carrying uploads and keep every ordinary one.
func TestSplitSkillUploadsDropsBaseOnOldWorker(t *testing.T) {
	in := []runner.XferUpload{
		{XferID: "xf-cwd", Dest: "in.txt"},                // Base "" — the pre-v10 reading
		{XferID: "xf-cwd2", Dest: "in2.txt", Base: "cwd"}, // explicit cwd
		{XferID: "xf-skill", Dest: "skills/a/SKILL.md", Base: "result_dir"},
	}

	keep, dropped := splitSkillUploads(in, 9, true)
	if dropped != 1 {
		t.Fatalf("proto 9 dropped = %d, want 1 (the skill mount)", dropped)
	}
	if len(keep) != 2 || keep[0].XferID != "xf-cwd" || keep[1].XferID != "xf-cwd2" {
		t.Fatalf("proto 9 keep = %+v, want both cwd uploads", keep)
	}

	keep, dropped = splitSkillUploads(in, 10, true)
	if dropped != 0 {
		t.Fatalf("proto 10 dropped = %d, want 0", dropped)
	}
	if len(keep) != 3 || keep[2].Base != "result_dir" {
		t.Fatalf("proto 10 keep = %+v, want all three with the mount intact", keep)
	}

	// An unknown protocol (offline worker, or a legacy connection with no recorded
	// version) is treated as "cannot carry": guessing wrong would write the skill into
	// the shared checkout.
	keep, dropped = splitSkillUploads(in, 10, false)
	if dropped != 1 || len(keep) != 2 {
		t.Fatalf("unknown protocol = %d kept / %d dropped, want 2/1", len(keep), dropped)
	}
	if keep[0].XferID != "xf-cwd" || keep[1].XferID != "xf-cwd2" {
		t.Fatalf("unknown protocol keep = %+v, want both cwd uploads", keep)
	}
}

// TestXferUploadsToWireCarriesBase guards the projection the negotiation depends on:
// the split decides on runner.XferUpload.Base, but the worker only ever sees the wire
// copy — losing Base there would send a skill mount as an ordinary cwd upload, which
// is the one outcome the whole v10 floor exists to prevent.
func TestXferUploadsToWireCarriesBase(t *testing.T) {
	got := xferUploadsToWire([]runner.XferUpload{
		{XferID: "xf-1", Dest: "in.txt"},
		{XferID: "xf-2", Dest: "skills/a/SKILL.md", Base: "result_dir"},
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Base != "" {
		t.Fatalf("got[0].Base = %q, want empty (cwd)", got[0].Base)
	}
	if got[1].Base != "result_dir" || got[1].Dest != "skills/a/SKILL.md" {
		t.Fatalf("got[1] = %+v, want the base preserved", got[1])
	}
	if xferUploadsToWire(nil) != nil {
		t.Fatal("nil in must stay nil (no allocation for a job with no uploads)")
	}
}
