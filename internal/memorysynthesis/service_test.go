package memorysynthesis

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

type staticSource []Item

func (s staticSource) Collect(context.Context, string, string) ([]Item, error) { return s, nil }

func openStore(t *testing.T) *jobstore.Store {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newService(st RunStore, items []Item, synth Synthesizer) *Service {
	var n atomic.Int64
	return &Service{
		Store: st, Sources: []Source{staticSource(items)}, Synth: synth, Loc: time.FixedZone("T", 8*3600),
		Timeout: time.Hour,
		Now:     func() time.Time { return time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC) },
		NewID:   func() string { return "run-" + string(rune('a'+n.Add(1)-1)) },
	}
}

var oneItem = []Item{{Kind: "work_item", SourceRef: "work:w-1", Title: "T", Text: "fact"}}

func okSynth(calls *atomic.Int32) Synthesizer {
	return func(_ context.Context, m Materials) (SynthResult, error) {
		calls.Add(1)
		return SynthResult{Agent: "fake", Model: "m", ResultJSON: `{"summary":"s","digest":"` + m.InputDigest[:8] + `"}`}, nil
	}
}

func TestWindowBoundaries(t *testing.T) {
	loc := time.FixedZone("T", 8*3600)
	w, err := DayWindow("2026-10-28", loc, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 28, 0, 0, 0, 0, loc).Unix()
	if !w.Contains(start) || w.Contains(start-1) || !w.Contains(start+86399) || w.Contains(start+86400) {
		t.Fatalf("window is not [00:00, next 00:00): %+v", w)
	}
	if w.Timezone() != "+08:00" {
		t.Fatalf("tz=%q", w.Timezone())
	}
	wide, _ := DayWindow("2026-10-28", loc, 72*time.Hour)
	if !wide.Contains(start-2*86400) || wide.Contains(start-3*86400) || wide.Contains(start+86400) {
		t.Fatalf("lookback window: %+v", wide)
	}
	short, _ := DayWindow("2026-10-28", loc, time.Hour)
	if short.Start.Unix() != start {
		t.Fatalf("a short lookback must not shrink the day: %+v", short)
	}
	if _, err := DayWindow("28/10/2026", loc, 0); err == nil {
		t.Fatal("bad day must fail")
	}
	// DST: the New York spring-forward day is 23 hours long.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	dst, _ := DayWindow("2026-03-08", ny, 0)
	if got := dst.End.Sub(dst.Start); got != 23*time.Hour {
		t.Fatalf("dst day length=%v", got)
	}
}

func TestRunSameDigestReusesSucceededRecord(t *testing.T) {
	st := openStore(t)
	var calls atomic.Int32
	svc := newService(st, oneItem, okSynth(&calls))
	first, err := svc.Run(context.Background(), "proj", "2026-10-28", "tester")
	if err != nil || first.State != OutcomeSucceeded || first.Run.Status != "succeeded" || first.Run.OutputDigest == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if first.Run.Attempt != 1 || first.Run.MaterialCountsJSON != `{"work_item":1}` || first.Run.MaterialSnapshot != "" {
		t.Fatalf("record=%+v", first.Run)
	}
	svc.Now = func() time.Time { return time.Date(2026, 10, 29, 5, 0, 0, 0, time.UTC) }
	second, err := svc.Run(context.Background(), "proj", "2026-10-28", "tester")
	if err != nil || second.State != OutcomeReused || second.Run.ID != first.Run.ID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("synthesizer ran %d times", calls.Load())
	}
	rows, _ := st.ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{ProjectKey: "proj"})
	if len(rows) != 1 || rows[0].LastCheckedAt != time.Date(2026, 10, 29, 5, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("last_checked_at must move without a new row: %+v", rows)
	}

	// Changed material is a new digest: a second run record, attempt 2.
	svc.Sources = []Source{staticSource(append(append([]Item{}, oneItem...), Item{Kind: "job_report", SourceRef: "job:j#report", Text: "more"}))}
	third, err := svc.Run(context.Background(), "proj", "2026-10-28", "tester")
	if err != nil || third.State != OutcomeSucceeded || third.Run.ID == first.Run.ID || third.Run.Attempt != 2 || calls.Load() != 2 {
		t.Fatalf("third=%+v err=%v calls=%d", third, err, calls.Load())
	}
}

func TestRunEmptyMaterialsCreateNoRecord(t *testing.T) {
	st := openStore(t)
	var calls atomic.Int32
	out, err := newService(st, nil, okSynth(&calls)).Run(context.Background(), "proj", "2026-10-28", "t")
	if err != nil || out.State != OutcomeEmpty || calls.Load() != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if rows, _ := st.ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{}); len(rows) != 0 {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestRunConcurrentCallsStartOneSynthesis(t *testing.T) {
	st := openStore(t)
	var calls atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	synth := func(ctx context.Context, m Materials) (SynthResult, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return SynthResult{ResultJSON: "{}"}, nil
	}
	svc := newService(st, oneItem, synth)
	var idN atomic.Int64
	svc.NewID = func() string { return "run-" + string(rune('a'+idN.Add(1))) }
	const n = 8
	var wg sync.WaitGroup
	states := make(chan string, n)
	first := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, err := svc.Run(context.Background(), "proj", "2026-10-28", "t")
		if err != nil {
			t.Errorf("run: %v", err)
		}
		states <- out.State
		close(first)
	}()
	<-entered // the first call now owns the running record
	for i := 1; i < n; i++ {
		out, err := svc.Run(context.Background(), "proj", "2026-10-28", "t")
		if err != nil || out.State != OutcomeRunning {
			t.Errorf("concurrent call %d: %+v err=%v", i, out, err)
		}
	}
	close(release)
	<-first
	wg.Wait()
	if got := <-states; got != OutcomeSucceeded || calls.Load() != 1 {
		t.Fatalf("state=%s calls=%d", got, calls.Load())
	}
}

func TestRunParallelRacersNeverDuplicate(t *testing.T) {
	st := openStore(t)
	var calls atomic.Int32
	svc := newService(st, oneItem, okSynth(&calls))
	var idN atomic.Int64
	svc.NewID = func() string { return "race-" + string(rune('a'+idN.Add(1))) }
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Run(context.Background(), "proj", "2026-10-28", "t"); err != nil {
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()
	rows, _ := st.ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{ProjectKey: "proj", Status: "all"})
	if len(rows) != 1 || rows[0].Status != "succeeded" || calls.Load() != 1 {
		t.Fatalf("rows=%+v calls=%d", rows, calls.Load())
	}
}

func TestRunFailureIsRecordedScrubbedAndRetried(t *testing.T) {
	st := openStore(t)
	fail := true
	var calls atomic.Int32
	synth := func(ctx context.Context, m Materials) (SynthResult, error) {
		calls.Add(1)
		if fail {
			return SynthResult{}, errors.New("boom token=abcd1234efgh")
		}
		return SynthResult{ResultJSON: "{}"}, nil
	}
	svc := newService(st, oneItem, synth)
	out, err := svc.Run(context.Background(), "proj", "2026-10-28", "t")
	if err != nil || out.State != OutcomeFailed || out.Run.Status != "failed" || strings.Contains(out.Run.Error, "abcd1234efgh") || !strings.Contains(out.Run.Error, "boom") {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	fail = false
	again, err := svc.Run(context.Background(), "proj", "2026-10-28", "t")
	if err != nil || again.State != OutcomeSucceeded || again.Run.Attempt != 2 || calls.Load() != 2 {
		t.Fatalf("a failed run must not block a retry: %+v err=%v", again, err)
	}

	// A panicking synthesizer is a failed run, not a crashed server.
	svc2 := newService(openStore(t), oneItem, func(context.Context, Materials) (SynthResult, error) { panic("kaput") })
	out, err = svc2.Run(context.Background(), "proj", "2026-10-28", "t")
	if err != nil || out.State != OutcomeFailed || !strings.Contains(out.Run.Error, "kaput") {
		t.Fatalf("panic out=%+v err=%v", out, err)
	}
}

func TestRunSnapshotOnlyWhenKept(t *testing.T) {
	items := []Item{{Kind: "work_item", SourceRef: "w", Text: "uses sk-abcdef1234567890abcdef here"}}
	var calls atomic.Int32
	st := openStore(t)
	svc := newService(st, items, okSynth(&calls))
	off, err := svc.Run(context.Background(), "proj", "2026-10-28", "t")
	if err != nil || off.Run.MaterialSnapshot != "" {
		t.Fatalf("snapshot must be empty by default: %+v err=%v", off.Run, err)
	}
	svc2 := newService(openStore(t), items, okSynth(&calls))
	svc2.KeepSnapshot = true
	on, err := svc2.Run(context.Background(), "proj", "2026-10-28", "t")
	if err != nil || on.Run.MaterialSnapshot == "" {
		t.Fatalf("snapshot missing: %+v err=%v", on.Run, err)
	}
	if strings.Contains(on.Run.MaterialSnapshot, "sk-abcdef") || !strings.Contains(on.Run.MaterialSnapshot, "[redacted]") {
		t.Fatalf("snapshot must be the scrubbed envelope: %s", on.Run.MaterialSnapshot)
	}
}

func TestStaleRunningRecordDoesNotBlockTheDay(t *testing.T) {
	st := openStore(t)
	var calls atomic.Int32
	svc := newService(st, oneItem, okSynth(&calls))
	m, _ := svc.Prepare(context.Background(), "proj", "2026-10-28")
	old := svc.Now().Add(-3 * time.Hour).Unix()
	if _, outcome, err := st.BeginMemorySynthesisIfChanged(jobstore.MemorySynthesisBegin{ID: "stuck", ProjectKey: "proj", Day: "2026-10-28",
		InputDigest: m.InputDigest, MaterialSchema: SchemaV1, Now: old}); err != nil || outcome != jobstore.MemorySynthesisStarted {
		t.Fatalf("seed: %v %v", outcome, err)
	}
	out, err := svc.Run(context.Background(), "proj", "2026-10-28", "t")
	if err != nil || out.State != OutcomeSucceeded {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	stuck, _, _ := st.GetMemorySynthesisRun("stuck")
	if stuck.Status != "failed" || stuck.Error != "timed out" {
		t.Fatalf("stuck=%+v", stuck)
	}
}
