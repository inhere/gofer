package job

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func seedFollowUpSource(t *testing.T, s *Service, root string, review bool) JobResult {
	t.Helper()
	req := JobRequest{ProjectKey: "self", Agent: "ca", Runner: "local", Cwd: ".", TimeoutSec: 30}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	src := JobResult{ID: "follow-source", ProjectKey: "self", Agent: "ca", Runner: "local", Cwd: ".", ResultDir: filepath.Join(root, "follow-source"), RequestJSON: string(raw), SessionID: "follow-session", Status: StatusDone, StartedAt: 1, EndedAt: 2, UpdatedAt: 2, TimeoutSec: 30, RequireReview: review}
	if err := s.Meta().UpsertJob(toRecord(src)); err != nil {
		t.Fatal(err)
	}
	return src
}

func waitFollowUpDone(t *testing.T, s *Service, id int64) jobstore.FollowUpRecord {
	t.Helper()
	var got jobstore.FollowUpRecord
	wait.For(t, 10*time.Second, "follow-up continuation", func() (bool, any) {
		rows, err := s.Meta().ListFollowUps("follow-source")
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if row.ID == id {
				got = row
				return row.Status == jobstore.FollowUpDone, row.Status
			}
		}
		return false, "missing"
	})
	return got
}

func TestFollowUpProcessMergesAndClaimsOnce(t *testing.T) {
	s, root := newContinueService(t)
	src := seedFollowUpSource(t, s, root, false)
	first, err := s.Meta().AddFollowUp(src.ID, "first", "caller", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Meta().AddFollowUp(src.ID, "second", "caller", false)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.processFollowUps(src) }()
	go func() { defer wg.Done(); s.processFollowUps(src) }()
	wg.Wait()
	one := waitFollowUpDone(t, s, first.ID)
	two := waitFollowUpDone(t, s, second.ID)
	if one.ContinuationID == "" || one.ContinuationID != two.ContinuationID {
		t.Fatalf("continuations = %q/%q", one.ContinuationID, two.ContinuationID)
	}
	cont, ok := s.Get(one.ContinuationID)
	if !ok {
		t.Fatalf("continuation %s missing", one.ContinuationID)
	}
	if cont.ResumedFrom != src.ID || cont.SessionID != src.SessionID {
		t.Fatalf("lineage = %+v", cont)
	}

	final, ok := s.Wait(cont.ID)
	if !ok || final.Status != StatusDone {
		t.Fatalf("continuation final = %+v/%v", final, ok)
	}
	if !containsAll(final.RenderedCommand, "first", "second") {
		t.Fatalf("merged continuation command = %q", final.RenderedCommand)
	}
}

func TestFollowUpReviewEachContinuationCarriesReview(t *testing.T) {
	s, root := newContinueService(t)
	src := seedFollowUpSource(t, s, root, false)
	item, err := s.Meta().AddFollowUp(src.ID, "review this", "caller", true)
	if err != nil {
		t.Fatal(err)
	}
	s.processFollowUps(src)
	row := waitFollowUpDone(t, s, item.ID)
	cont, ok := s.Get(row.ContinuationID)
	if !ok {
		t.Fatal("review_each continuation missing")
	}
	if !cont.RequireReview {
		t.Fatalf("review_each continuation require_review = false")
	}
}

func TestFollowUpFailureKeepsQueueAndDropPreventsExecution(t *testing.T) {
	s, root := newContinueService(t)
	src := seedFollowUpSource(t, s, root, false)
	failed := src
	failed.Status = StatusFailed
	item, err := s.Meta().AddFollowUp(src.ID, "do not run", "caller", false)
	if err != nil {
		t.Fatal(err)
	}
	s.processFollowUps(failed)
	rows, _ := s.Meta().ListFollowUps(src.ID)
	if len(rows) != 1 || rows[0].Status != jobstore.FollowUpPending {
		t.Fatalf("failed queue = %+v", rows)
	}
	if ok, err := s.Meta().DropFollowUp(src.ID, item.ID); err != nil || !ok {
		t.Fatalf("drop = %v/%v", ok, err)
	}
	s.processFollowUps(src)
	rows, _ = s.Meta().ListFollowUps(src.ID)
	if rows[0].Status != jobstore.FollowUpDropped {
		t.Fatalf("dropped queue = %+v", rows)
	}
}

func containsAll(s string, values ...string) bool {
	for _, v := range values {
		if !strings.Contains(s, v) {
			return false
		}
	}
	return true
}
