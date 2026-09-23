package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
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

// TestUnsupportedDispatchFieldsIgnoresSkillUploads: the capability gate must not
// start refusing jobs over skills. A job whose ONLY uploads are the skill mount is
// dispatched (minus the mount) even to a worker below the file-transfer floor,
// because dropping a skill costs the job nothing it needs; an ordinary input upload
// on the same worker is still refused, because losing THAT would run the job without
// the files the caller supplied.
func TestUnsupportedDispatchFieldsIgnoresSkillUploads(t *testing.T) {
	skillOnly := &runner.Forward{Uploads: []runner.XferUpload{
		{XferID: "xf-1", Dest: "skills/a/SKILL.md", Base: "result_dir"},
	}}
	if lacks := unsupportedDispatchFields(8, skillOnly); len(lacks) != 0 {
		t.Fatalf("lacks = %v, want none: a skill mount is dropped, not refused", lacks)
	}

	withInput := &runner.Forward{Uploads: []runner.XferUpload{
		{XferID: "xf-2", Dest: "in.txt"},
	}}
	if lacks := unsupportedDispatchFields(8, withInput); len(lacks) != 1 || lacks[0] != "uploads/collect" {
		t.Fatalf("lacks = %v, want [uploads/collect] for a real input file", lacks)
	}
}

// TestOldWorkerGetsNoManifest (决策 1, 2026-09-23): the prompt list is rendered by the
// machine that MOUNTS the files, so a peer that predates the upload base gets neither
// the mount nor the names — a worker that cannot be given the files must never be told
// to read them. The job still runs; the omission is job.skills_skipped{worker_protocol}.
func TestOldWorkerGetsNoManifest(t *testing.T) {
	h := &fakeHub{workerProto: 9}
	r := newRunnerWithHub(h)

	type jobEvent struct {
		typ    string
		detail map[string]any
	}
	var (
		mu     sync.Mutex
		events []jobEvent
	)
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID: "j1",
			Forward: &runner.Forward{
				ProjectKey: "p", Agent: "omp",
				Prompt: "ORIGINAL-PROMPT",
				Skills: []string{"house-rules"},
				Uploads: []runner.XferUpload{
					{XferID: "xf-skill", Dest: "skills/house-rules/SKILL.md", Base: "result_dir"},
				},
			},
			OnJobEvent: func(eventType string, detail map[string]any) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, jobEvent{eventType, detail})
			},
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Finish")
	}

	d := h.dispatchedFrame()
	if strings.Contains(d.Prompt, "可用技能") {
		t.Fatalf("the dispatch carries a skills list the old worker cannot honour:\n%s", d.Prompt)
	}
	if d.Prompt != "ORIGINAL-PROMPT" {
		t.Fatalf("dispatch prompt = %q, want the caller's text untouched", d.Prompt)
	}
	if len(d.Skills) != 0 {
		t.Fatalf("dispatch skills = %v, want none for a worker that cannot carry the mount", d.Skills)
	}
	if len(d.Uploads) != 0 {
		t.Fatalf("dispatch uploads = %+v, want the mount dropped", d.Uploads)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly the skip report", events)
	}
	if events[0].typ != runner.EventSkillsSkipped {
		t.Fatalf("event = %q, want %q", events[0].typ, runner.EventSkillsSkipped)
	}
	if events[0].detail["reason"] != "worker_protocol" {
		t.Fatalf("event detail = %+v, want reason worker_protocol", events[0].detail)
	}
}
