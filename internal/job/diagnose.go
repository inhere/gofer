package job

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// Job diagnosis (R10): one read-only answer to "why did this job end, what else was in
// the chain, when did each stage happen, and what should I do next?". Everything here is
// DERIVED from fields and rows gofer already keeps (the job snapshot, its events, its
// durable retries, its log files) — no new column, nothing re-run, nothing invented: a
// stage gofer never recorded is reported as unknown instead of being back-filled from a
// neighbouring timestamp.

// Diagnosis is the whole answer of one diagnose run. Field names are part of the HTTP
// contract (GET /v1/jobs/{id}/diagnosis) and of `gofer job diagnose --json`.
type Diagnosis struct {
	JobID    string `json:"job_id"`
	Status   string `json:"status"`
	Finished bool   `json:"finished"`
	// Reason is the TerminationReason enum value; ReasonLabel is its localized label.
	Reason      string `json:"termination_reason"`
	ReasonLabel string `json:"termination_label"`
	// Error is the job's own failure text (may be multi-line), first line in Chain links.
	Error string `json:"error,omitempty"`
	// Chain is every job found through the continuation links (resume, auto-resume,
	// failover, durable retry), oldest first. FirstFailure is the EARLIEST job in that
	// chain that ended badly — the origin the later attempts inherited.
	Chain        []DiagnosisLink `json:"chain"`
	FirstFailure *DiagnosisLink  `json:"first_failure,omitempty"`
	// Timeline holds the five stages in order; a stage gofer has no record of is
	// Present=false with Note saying why, never a guessed time.
	Timeline  []DiagnosisStage `json:"timeline"`
	Evidence  []string         `json:"evidence"`
	Unknowns  []string         `json:"unknowns"`
	NextSteps []string         `json:"next_steps"`
}

// DiagnosisLink is one job of the continuation chain.
type DiagnosisLink struct {
	JobID string `json:"job_id"`
	// Relation of this link to the diagnosed job: target / source / resume / failover /
	// retry. It says HOW the link was found, which is what makes the chain readable.
	Relation    string `json:"relation"`
	Status      string `json:"status"`
	Reason      string `json:"termination_reason"`
	Error       string `json:"error,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	EndedAt     int64  `json:"ended_at,omitempty"`
}

// DiagnosisStage is one point of the job's lifecycle.
type DiagnosisStage struct {
	Stage string `json:"stage"`
	Label string `json:"label"`
	At    int64  `json:"at,omitempty"`
	// Present is false when gofer has no record of this stage for this job.
	Present bool   `json:"present"`
	Note    string `json:"note,omitempty"`
}

// Stage names of the timeline.
const (
	DiagnosisStageQueued      = "queued"
	DiagnosisStageStarted     = "started"
	DiagnosisStageFirstOutput = "first_output"
	DiagnosisStageLastOutput  = "last_output"
	DiagnosisStageEnded       = "ended"
)

// chainLimit bounds how many jobs one diagnose run will follow, so a pathological chain
// (or a cycle written by an old bug) cannot turn into an unbounded walk.
const chainLimit = 50

// Diagnose builds the diagnosis of job id in the project's own language. An unknown id
// returns ErrJobNotFound (the HTTP layer maps it to 404).
func (s *Service) Diagnose(ctx context.Context, id string) (Diagnosis, error) {
	res, ok := s.Get(id)
	if !ok {
		return Diagnosis{}, ErrJobNotFound
	}
	return s.diagnose(ctx, s.notifyLang(res.ProjectKey), id)
}

// DiagnoseIn is Diagnose in an explicit language: the HTTP layer passes the request's
// language so the same row explains itself in the caller's language.
func (s *Service) DiagnoseIn(lang i18n.Lang, ctx context.Context, id string) (Diagnosis, error) {
	if _, ok := s.Get(id); !ok {
		return Diagnosis{}, ErrJobNotFound
	}
	return s.diagnose(ctx, lang, id)
}

func (s *Service) diagnose(ctx context.Context, lang i18n.Lang, id string) (Diagnosis, error) {
	res, ok := s.Get(id)
	if !ok {
		return Diagnosis{}, ErrJobNotFound
	}
	d := Diagnosis{
		JobID:       res.ID,
		Status:      res.Status,
		Finished:    IsFinished(res.Status),
		Reason:      TerminationReason(res),
		ReasonLabel: TerminationLabelIn(lang, TerminationReason(res)),
		Error:       res.Error,
	}
	d.Chain = s.diagnoseChain(id)
	d.FirstFailure = firstFailingLink(d.Chain, id)
	d.Timeline = s.diagnoseTimeline(lang, res)
	d.Evidence = diagnosesEvidence(res)
	d.NextSteps = []string{i18n.T(lang, "job.diagnose.next."+d.Reason)}
	d.Unknowns = diagnoseUnknowns(lang, d)
	return d, nil
}

// diagnoseChain walks the continuation links in both directions from id and returns the
// jobs it reaches, oldest first. Each link records how it was reached; the starting job
// is always "target" and always present.
func (s *Service) diagnoseChain(id string) []DiagnosisLink {
	return diagnoseChainWith(id,
		func(jobID string) (JobResult, bool) { return s.Get(jobID) },
		func(jobID string) []jobstore.RetryRecord {
			rows, err := s.ListRetries(jobID)
			if err != nil {
				return nil
			}
			return rows
		})
}

// diagnoseChainWith is the pure part of diagnoseChain: it takes the lookups as funcs so
// the chain logic (which links exist, the order, the earliest failure) is testable
// without a service, a store or a running job.
func diagnoseChainWith(id string, get func(string) (JobResult, bool), retries func(string) []jobstore.RetryRecord) []DiagnosisLink {
	type node struct {
		id       string
		relation string
	}
	seen := map[string]bool{id: true}
	queue := []node{{id: id, relation: "target"}}
	var out []DiagnosisLink
	for len(queue) > 0 && len(out) < chainLimit {
		cur := queue[0]
		queue = queue[1:]
		res, ok := get(cur.id)
		if !ok {
			// A link that no longer resolves (a deleted job) is still worth naming: it
			// explains a gap in the chain instead of silently shortening it.
			out = append(out, DiagnosisLink{JobID: cur.id, Relation: cur.relation, Status: "missing"})
			continue
		}
		link := DiagnosisLink{
			JobID:     res.ID,
			Relation:  cur.relation,
			Status:    res.Status,
			Reason:    TerminationReason(res),
			Error:     firstLine(res.Error),
			Attempt:   res.AutoResumeAttempt,
			StartedAt: res.StartedAt,
			EndedAt:   res.EndedAt,
		}
		out = append(out, link)
		// Neighbours carry their own relation to THIS job: going up the chain is a
		// "source", coming down is a "resume" (auto-resume / failover / retry alike).
		up := []struct{ id, relation string }{
			{res.ResumedFrom, "resume"},
			{res.AutoResumedBy, "resume"},
			{res.SourceJobID, "source"},
			{res.FellBackFrom, "failover"},
			{res.FellBackTo, "failover"},
		}
		for _, r := range retries(res.ID) {
			up = append(up, struct{ id, relation string }{r.SourceJobID, "retry"}, struct{ id, relation string }{r.NewJobID, "retry"})
		}
		for _, n := range up {
			if n.id == "" || seen[n.id] {
				continue
			}
			seen[n.id] = true
			queue = append(queue, node{id: n.id, relation: n.relation})
		}
	}
	// Oldest first: the chain reads in the order it happened. A job without a start time
	// sorts by id so the order stays deterministic.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].JobID < out[j].JobID
	})
	return out
}

// firstFailingLink returns the EARLIEST link that ended badly — the origin of the chain's
// failure, which is what a person should read first. The diagnosed job itself is skipped
// only when another failing link exists earlier; a lone failure is still reported.
func firstFailingLink(chain []DiagnosisLink, target string) *DiagnosisLink {
	for i := range chain {
		l := chain[i]
		if l.JobID == target || !IsFinished(l.Status) {
			continue
		}
		if l.Reason == TerminationUnknown || l.Reason == TerminationOK {
			continue
		}
		cp := l
		return &cp
	}
	return nil
}

// diagnoseTimeline reads the five stages off the job row and its log files. Output stages
// are answered from the logs' modification times, which is the only record gofer keeps of
// them; a job with no log file answers "unknown" rather than borrowing another stage.
func (s *Service) diagnoseTimeline(lang i18n.Lang, res JobResult) []DiagnosisStage {
	stages := []DiagnosisStage{
		diagnosisStage(lang, DiagnosisStageQueued, res.QueuedAt, "job.diagnose.unknown_queued"),
		diagnosisStage(lang, DiagnosisStageStarted, res.StartedAt, "job.diagnose.unknown_started"),
		{Stage: DiagnosisStageFirstOutput, Label: i18n.T(lang, "job.diagnose.stage."+DiagnosisStageFirstOutput),
			Note: i18n.T(lang, "job.diagnose.unknown_first_output")},
	}
	last, okLog := s.lastOutputAt(res)
	stages = append(stages,
		diagnosisStage(lang, DiagnosisStageLastOutput, lastAt(last, okLog), "job.diagnose.unknown_last_output"),
		diagnosisStage(lang, DiagnosisStageEnded, res.EndedAt, "job.diagnose.unknown_ended"),
	)
	return stages
}

// diagnosisStage builds one stage: Present when at > 0, otherwise a localized note on why
// there is nothing to show.
func diagnosisStage(lang i18n.Lang, stage string, at int64, unknownKey string) DiagnosisStage {
	st := DiagnosisStage{Stage: stage, Label: i18n.T(lang, "job.diagnose.stage."+stage)}
	if at > 0 {
		st.At = at
		st.Present = true
		return st
	}
	st.Note = i18n.T(lang, unknownKey)
	return st
}

// lastOutputAt is the modification time of the job's newest stdout/stderr log — the only
// recorded evidence that the job produced output, and when it last did.
func (s *Service) lastOutputAt(res JobResult) (int64, bool) {
	if res.ResultDir == "" {
		return 0, false
	}
	var newest int64
	for _, name := range []string{store.StdoutFile, store.StderrFile} {
		fi, err := os.Stat(filepath.Join(res.ResultDir, name))
		if err != nil {
			continue
		}
		if ts := fi.ModTime().Unix(); ts > newest {
			newest = ts
		}
	}
	return newest, newest > 0
}

func lastAt(at int64, ok bool) int64 {
	if !ok {
		return 0
	}
	return at
}

// diagnosesEvidence lists what the row itself proves, in a fixed order, so two diagnoses
// of the same job read the same. Empty fields are omitted (no "failure_class=<empty>").
func diagnosesEvidence(res JobResult) []string {
	var out []string
	out = append(out, "status="+res.Status)
	out = append(out, "exit_code="+strconv.Itoa(res.ExitCode))
	if res.FailureClass != "" {
		out = append(out, "failure_class="+res.FailureClass)
	}
	if res.WrapUp != "" {
		out = append(out, "wrap_up="+res.WrapUp)
	}
	if res.StopReason != "" {
		out = append(out, "stop_reason="+res.StopReason)
	}
	if res.SessionEndReason != "" {
		out = append(out, "session_end_reason="+res.SessionEndReason)
	}
	if res.TimeoutSec > 0 {
		ev := "timeout_sec=" + strconv.Itoa(res.TimeoutSec)
		if res.TimeoutLimitKey != "" {
			ev += " (ceiling " + res.TimeoutLimitKey + ")"
		}
		out = append(out, ev)
	}
	if res.WorkerID != "" {
		out = append(out, "worker="+res.WorkerID)
	}
	if res.WorktreePath != "" {
		out = append(out, "worktree="+res.WorktreePath)
	}
	if line := firstLine(res.Error); line != "" {
		out = append(out, "error="+line)
	}
	return out
}

// diagnoseUnknowns names what this diagnosis could NOT answer. It is derived from the
// timeline and the chain, so a complete record produces no noise.
func diagnoseUnknowns(lang i18n.Lang, d Diagnosis) []string {
	var out []string
	for _, st := range d.Timeline {
		if !st.Present {
			out = append(out, st.Label+": "+st.Note)
		}
	}
	if d.FirstFailure == nil && len(d.Chain) > 1 {
		out = append(out, i18n.T(lang, "job.diagnose.first_failure_none"))
	}
	if !d.Finished {
		out = append(out, i18n.T(lang, "job.diagnose.still_running"))
	}
	return out
}

// firstLine is the first non-empty line of s — the one-line summary of a failure used in
// the chain and the evidence list.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
