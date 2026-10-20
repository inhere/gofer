package serve

import (
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/work"
	"github.com/inhere/gofer/internal/wshub"
)

// decisionExpirySweepEvery is how often due decisions are actively expired. Reads
// already expire lazily; the sweep exists so the browser's bell hears about a deadline
// that passed while nobody was looking.
const decisionExpirySweepEvery = 5 * time.Second

// wireLivePush connects every notification source to the browser push hub (Q2). Each
// hook only enqueues (pushhub never blocks a publisher), so none of them can slow
// recordEvent, persist or a worker connection.
//
//	store writes  -> jobs / job:<id> / stats / pending / sessions / plans / workflows / schedules
//	event tap     -> job:<id> increments + the topics a given event type touches
//	worker online -> runners / stats
//
// jobs and wh may be nil (tests, hub-less assembly).
//
// The hub has a single presence-observer slot, so other consumers of worker presence
// (worker enrollment's first-connect fallback) pass their callback as presence...; they
// are fanned out from the one observer and, like the push hook, must only enqueue.
func wireLivePush(ph *pushhub.Hub, store *jobstore.Store, jobs *job.Service, wh *wshub.Hub, ws *work.Service, presence ...func(workerID string, online bool)) {
	if ph == nil {
		return
	}
	if store != nil {
		store.SetChangeHook(func(ch jobstore.Change) {
			switch ch.Kind {
			case jobstore.ChangeJob:
				ph.NotifyJobProject(ch.ID, ch.Status, ch.Project) // jobs inval + job:<id> status + stats
				ws.MarkDirty()                                    // a linked job may have reached needs_review
			case jobstore.ChangeDecision:
				ph.NotifyProject(pushhub.TopicPending, ch.Project)
				ph.NotifyProject(pushhub.TopicStats, ch.Project)
				ph.NotifyProject(pushhub.TopicSessions, ch.Project) // a relay turn is a decision on a session
				ws.MarkDirty()
			case jobstore.ChangeInteraction:
				ph.NotifyProject(pushhub.TopicPending, ch.Project)
				ph.NotifyProject(pushhub.TopicStats, ch.Project)
				if ch.ID != "" {
					// 7798 第二期: the hint carries the change's project, so a restricted connection
					// only hears it for a job of its own projects.
					ph.PublishJobKindProject(ch.ID, "interaction", ch.Project)
				}
				ws.MarkDirty() // a pending interaction means "needs me"
			case jobstore.ChangeSession:
				ph.NotifyProject(pushhub.TopicSessions, ch.Project)
				ph.NotifyProject(pushhub.TopicStats, ch.Project)
				ws.MarkDirty() // session state drives the work item's status
			case jobstore.ChangePlan:
				ph.NotifyProject(pushhub.TopicPlans, ch.Project)
				ws.MarkDirty() // a linked todo may have finished (completion write-back)
			case jobstore.ChangeWorkflow:
				ph.NotifyProject(pushhub.TopicWorkflows, ch.Project)
			case jobstore.ChangeWork:
				ph.NotifyProject(pushhub.TopicWork, ch.Project)
			case jobstore.ChangeSchedule:
				ph.NotifyProject(pushhub.TopicSchedules, ch.Project)
				ph.NotifyProject(pushhub.TopicStats, ch.Project)
			case jobstore.ChangeWorkerEnrollment:
				ph.Notify(pushhub.TopicWorkerEnrollments) // pure invalidation, no payload, no project
			}
		})
	}
	if jobs != nil {
		// A restricted push connection binds the project of the job topics it subscribes
		// to (pushhub SetJobProject); a job it cannot resolve stays unbound there, and the
		// subscription itself is gated by the caller's scope in httpapi.
		ph.SetJobProject(func(id string) (string, bool) {
			j, ok := jobs.Get(id)
			if !ok || j.ProjectKey == "" {
				return "", false
			}
			return j.ProjectKey, true
		})
		jobs.SetEventTap(func(scope string, ev jobstore.JobEvent) {
			ph.NoteEvent(scope, pushhub.JobEvent{Seq: ev.Seq, Type: ev.Type, Detail: ev.Detail, At: ev.At})
		})
	}
	if wh != nil {
		wh.SetPresenceObserver(func(id string, online bool) {
			ph.Notify(pushhub.TopicRunners)
			ph.Notify(pushhub.TopicStats)
			for _, fn := range presence {
				if fn != nil {
					fn(id, online)
				}
			}
		})
		// Heartbeats refresh last_heartbeat / in_flight / messenger / dirs; the hub
		// throttles them per worker (BeatNotifyInterval), so this is cheap.
		wh.SetHeartbeatObserver(func(string) { ph.Notify(pushhub.TopicRunners) })
	}
}

// startDecisionExpiryLoop expires due decisions on a timer until stop closes. The
// expiry itself notifies through the store's change hook.
func startDecisionExpiryLoop(store *jobstore.Store, every time.Duration, stop <-chan struct{}) {
	if store == nil {
		return
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = store.ExpireDueDecisions()
			}
		}
	}()
}

// sessionOfflineSweepEvery is the cadence of the silent-session sweep.
const sessionOfflineSweepEvery = 60 * time.Second

// sweepSessionsOffline marks sessions silent for longer than afterSec offline (the
// store skips sessions parked on an open relay turn). afterSec <= 0 disables it.
func sweepSessionsOffline(store *jobstore.Store, afterSec int, now time.Time) (int64, error) {
	if store == nil || afterSec <= 0 {
		return 0, nil
	}
	return store.MarkStaleSessionsOffline(now.Unix() - int64(afterSec))
}

// startSessionOfflineLoop runs the offline sweep until stop closes. afterSec is read on
// every tick, so a hot-reloaded session.offline_after_sec applies to the next sweep.
func startSessionOfflineLoop(store *jobstore.Store, afterSec func() int, every time.Duration, stop <-chan struct{}) {
	if store == nil {
		return
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_, _ = sweepSessionsOffline(store, afterSec(), time.Now())
			}
		}
	}()
}
