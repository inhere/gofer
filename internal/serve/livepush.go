package serve

import (
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
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
func wireLivePush(ph *pushhub.Hub, store *jobstore.Store, jobs *job.Service, wh *wshub.Hub) {
	if ph == nil {
		return
	}
	if store != nil {
		store.SetChangeHook(func(ch jobstore.Change) {
			switch ch.Kind {
			case jobstore.ChangeJob:
				ph.NotifyJob(ch.ID, ch.Status) // jobs inval + job:<id> status + stats
			case jobstore.ChangeDecision:
				ph.Notify(pushhub.TopicPending)
				ph.Notify(pushhub.TopicStats)
				ph.Notify(pushhub.TopicSessions) // a relay turn is a decision on a session
			case jobstore.ChangeInteraction:
				ph.Notify(pushhub.TopicPending)
				ph.Notify(pushhub.TopicStats)
				if ch.ID != "" {
					ph.PublishJobKind(ch.ID, "interaction")
				}
			case jobstore.ChangeSession:
				ph.Notify(pushhub.TopicSessions)
				ph.Notify(pushhub.TopicStats)
			case jobstore.ChangePlan:
				ph.Notify(pushhub.TopicPlans)
			case jobstore.ChangeWorkflow:
				ph.Notify(pushhub.TopicWorkflows)
			case jobstore.ChangeSchedule:
				ph.Notify(pushhub.TopicSchedules)
				ph.Notify(pushhub.TopicStats)
			}
		})
	}
	if jobs != nil {
		jobs.SetEventTap(func(scope string, ev jobstore.JobEvent) {
			ph.NoteEvent(scope, pushhub.JobEvent{Seq: ev.Seq, Type: ev.Type, Detail: ev.Detail, At: ev.At})
		})
	}
	if wh != nil {
		wh.SetPresenceObserver(func(string, bool) {
			ph.Notify(pushhub.TopicRunners)
			ph.Notify(pushhub.TopicStats)
		})
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
