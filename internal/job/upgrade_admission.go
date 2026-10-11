package job

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/inhere/gofer/internal/config"
)

var ErrUpgradeDraining = errors.New("server upgrade is draining new work")

// AdmissionPermit protects an entire internal claim/advance plus its resulting
// Submit. A permit belongs to one Service and cannot be serialized or forged by
// a job request. Call Release once the durable producer transition is complete.
type AdmissionPermit struct {
	owner    *Service
	once     sync.Once
	released atomic.Bool
}

// Released lets an asynchronous workflow callback discard an expired admitted
// view and acquire a fresh permit through its original engine.
func (p *AdmissionPermit) Released() bool { return p == nil || p.released.Load() }

func (p *AdmissionPermit) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.released.Store(true)
		s := p.owner
		s.admissionMu.Lock()
		s.admissionInFlight--
		if s.admissionInFlight == 0 && s.admissionIdle != nil {
			close(s.admissionIdle)
			s.admissionIdle = nil
		}
		s.admissionMu.Unlock()
	})
}

func (s *Service) BeginUpgradeWork() (*AdmissionPermit, error) {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if s.admissionClosed {
		return nil, ErrUpgradeDraining
	}
	if s.admissionInFlight == 0 {
		s.admissionIdle = make(chan struct{})
	}
	s.admissionInFlight++
	return &AdmissionPermit{owner: s}, nil
}

// CloseUpgradeAdmission prevents new producers and waits for every accepted
// submission/claim to finish. A timeout leaves admission closed until the bridge
// explicitly reopens it after its failed upgrade transaction is reconciled.
func (s *Service) CloseUpgradeAdmission(ctx context.Context, upgradeID string) error {
	if upgradeID == "" {
		return errors.New("upgrade id required")
	}
	s.admissionMu.Lock()
	if s.admissionClosed && s.admissionUpgradeID != upgradeID {
		s.admissionMu.Unlock()
		return fmt.Errorf("another upgrade owns admission gate")
	}
	s.admissionClosed = true
	s.admissionUpgradeID = upgradeID
	idle := s.admissionIdle
	s.admissionMu.Unlock()
	if idle == nil {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) OpenUpgradeAdmission(upgradeID string) error {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if !s.admissionClosed {
		return nil
	}
	if s.admissionUpgradeID != upgradeID {
		return fmt.Errorf("another upgrade owns admission gate")
	}
	s.admissionClosed = false
	s.admissionUpgradeID = ""
	return nil
}

func (s *Service) UpgradeAdmissionOwner() string {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	if !s.admissionClosed {
		return ""
	}
	return s.admissionUpgradeID
}

// SubmitWithPermit continues an already admitted internal producer. Callers
// must hold the permit across their prior durable claim and this submission.
func (s *Service) SubmitWithPermit(permit *AdmissionPermit, req JobRequest) (JobResult, error) {
	if permit == nil || permit.owner != s || permit.released.Load() {
		return JobResult{}, errors.New("invalid or released upgrade admission permit")
	}
	s.admissionMu.Lock()
	active := s.admissionInFlight > 0
	s.admissionMu.Unlock()
	if !active {
		return JobResult{}, errors.New("released upgrade admission permit")
	}
	return s.submitAdmitted(req)
}

// UpgradeIdleSessionIDs returns the resident ACP jobs that may stay open across
// a server restart, so the upgrade drain does not wait for them:
//   - local sessions the shutdown path preserves and recovers with session/load;
//   - worker sessions: the ACP process belongs to the worker, which keeps it
//     across a server restart; the row is held as recovering and adopted when the
//     same worker process reconnects (it needs a recorded instance id).
//
// Only idle sessions qualify (awaiting input, nothing queued, not ending). The
// bridge calls this after closing admission, so SaySession cannot queue another
// turn during the census.
func (s *Service) UpgradeIdleSessionIDs() ([]string, error) {
	s.admissionMu.Lock()
	closed := s.admissionClosed && s.admissionInFlight == 0
	s.admissionMu.Unlock()
	if !closed {
		return nil, errors.New("close admission before classifying resident sessions")
	}
	cfg := s.cfg.Load()
	s.mu.Lock()
	entries := make(map[string]*jobEntry, len(s.jobs))
	for id, entry := range s.jobs {
		entries[id] = entry
	}
	s.mu.Unlock()
	ids := make([]string, 0)
	for id, entry := range entries {
		entry.mu.Lock()
		idle := entry.result.Session && entry.result.Status == StatusAwaitingInput &&
			!entry.sessionCommandPending && !entry.sessionEnding && !entry.shutdownRequested
		local := idle && config.IsLocalRunnerName(entry.result.Runner) &&
			entry.sessionCommands != nil && entry.result.SessionID != ""
		remote := idle && entry.sessionCommands == nil && isWorkerRemoteSession(cfg, entry.result)
		entry.mu.Unlock()
		if !local && !remote {
			continue
		}
		rec, ok, err := s.meta.GetJob(id)
		if err != nil {
			return nil, err
		}
		if !ok || rec.Status != StatusAwaitingInput {
			continue
		}
		if local && (!config.IsLocalRunnerName(rec.Runner) || rec.SessionStateJSON == "") {
			continue
		}
		if remote && (rec.WorkerID == "" || rec.WorkerInstanceID == "") {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}
