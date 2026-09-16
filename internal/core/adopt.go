package core

import (
	"encoding/json"
	"log/slog"

	"github.com/inhere/gofer/internal/job"
	workerrunner "github.com/inhere/gofer/internal/runner/worker"
	"github.com/inhere/gofer/internal/wshub"
	"github.com/inhere/gofer/internal/wsproto"
)

// jobAdopter adapts the job service's RECOV-01 R4 adoption seam onto the hub's
// wshub.Adopter contract. It lives in the assembly layer (like hubWorkerSelector /
// agentBriefsFromSnapshot) because it is the ONLY place allowed to know both sides:
// the hub must not import the job store, and the job service must not import the wire
// protocol.
type jobAdopter struct {
	hub  *wshub.Hub
	jobs *job.Service
}

// NewJobAdopter builds the adoption seam that lets a hub starting after a serve
// restart adopt the `recovering` jobs a PREVIOUS serve process left in the store. It is
// exported so a harness standing up the same hub+job pair wires exactly what the server
// wires.
func NewJobAdopter(hub *wshub.Hub, jobs *job.Service) wshub.Adopter {
	return &jobAdopter{hub: hub, jobs: jobs}
}

// AdoptAfterRestart implements wshub.Adopter: it projects the worker's wire inflight
// report onto the job package's neutral vocabulary, lets the job service reconcile the
// store, and wraps every adopted handle into a wshub.JobSink. The transport half of an
// adopted job (interaction answers, cancel frames) is injected here — it is the only
// piece that needs the hub connection, which the job service deliberately cannot see.
func (a *jobAdopter) AdoptAfterRestart(workerID, instanceID string, inflight []wsproto.InflightJob) (map[string]wshub.AdoptedJob, []string) {
	inf := make([]job.WorkerInflightJob, 0, len(inflight))
	for _, f := range inflight {
		inf = append(inf, job.WorkerInflightJob{JobID: f.JobID, Status: f.Status})
	}
	backend := job.AdoptBackend{
		Answer: func(jobID, interactionID, answer string) {
			if err := a.hub.Answer(workerID, jobID, interactionID, answer); err != nil {
				slog.Warn("adopted job answer not delivered", "component", "server",
					"worker_id", workerID, "job_id", jobID, "err", err)
			}
		},
		Cancel: func(jobID string) {
			// Best-effort, like every other cancel relay: the host job is already
			// terminal on its own account, so a lost frame only costs the worker some
			// wasted work.
			if err := a.hub.Cancel(workerID, jobID); err != nil {
				slog.Warn("adopted job cancel not delivered", "component", "server",
					"worker_id", workerID, "job_id", jobID, "err", err)
			}
		},
	}
	adopted, lost := a.jobs.ReconcileAdoption(workerID, instanceID, inf, backend)
	if len(adopted) == 0 {
		return nil, lost
	}
	out := make(map[string]wshub.AdoptedJob, len(adopted))
	for id, aj := range adopted {
		stdoutOff, stderrOff := aj.Offsets()
		out[id] = wshub.AdoptedJob{
			Sink:      &adoptSink{aj: aj, workerID: workerID},
			StdoutOff: stdoutOff,
			StderrOff: stderrOff,
		}
	}
	return out, lost
}

// adoptSink projects an adopted job handle onto wshub.JobSink. The wire half of the
// translation is exactly the dispatched worker sink's (ResultErr / OutcomeFrom), so an
// adopted frame is finished by the same mapping as a frame for a job this process
// dispatched itself.
type adoptSink struct {
	aj       *job.AdoptedJob
	workerID string
}

func (s *adoptSink) WriteLog(stream string, seq int, text string) { s.aj.WriteLog(stream, seq, text) }

func (s *adoptSink) OnInteraction(action string, interaction json.RawMessage) {
	s.aj.OnInteraction(action, interaction)
}

// OnOutcome always routes the frame through OutcomeFrom (even a nil projection — a
// frame with no产出 at all) so an adopted job's outcome bookkeeping matches a
// dispatched one's, including the G1 early rendered-command push.
func (s *adoptSink) OnOutcome(o wsproto.Outcome) {
	if oc := workerrunner.OutcomeFrom(&o, s.workerID); oc != nil {
		s.aj.OnOutcome(*oc)
	}
}

func (s *adoptSink) Finish(res wsproto.Result) {
	s.aj.Finish(res.ExitCode, workerrunner.ResultErr(res))
}

func (s *adoptSink) Suspend(reason string) (int64, int64) { return s.aj.Suspend(reason) }

func (s *adoptSink) Resume() { s.aj.Resume() }

func (s *adoptSink) OnDisconnect(err error) { s.aj.OnDisconnect(err) }
