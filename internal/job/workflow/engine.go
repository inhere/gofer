package workflow

import (
	"time"

	"github.com/inhere/gofer/internal/config"
	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// JobOps is the narrow capability surface the workflow engine needs from the host
// job service (layering design §13.3). job.Service satisfies it (WS1 accessors).
type JobOps interface {
	Submit(req job.JobRequest) (job.JobResult, error)
	Cancel(id string) error
	Validate(cfg *config.Config, req job.JobRequest, remote bool) (config.ProjectConfig, error)
	Config() *config.Config
	Meta() *jobstore.Store
	Now() time.Time
	Metrics() job.MetricsSink
	// Get / TailLog are the single-job read surface ref resolution needs (refs.go:
	// ${steps.N.result|stdout|...}). They are existing exported job.Service methods
	// pulled into JobOps so the engine resolves a prior step's output through the
	// SAME live-in-memory-then-DB path as before (zero behaviour change); they are
	// not a new capability — refs.go already called s.Get / s.TailLog in package job.
	Get(id string) (job.JobResult, bool)
	TailLog(id string, stream store.Stream, maxBytes int64) ([]byte, error)
	// Wait blocks until a job reaches a terminal state; used only by workflow tests to
	// drain a background step-job before teardown (existing exported job.Service method).
	Wait(id string) (job.JobResult, bool)
}

// Engine runs job-chain workflows over the host job service (design §13). All logic
// is moved verbatim from package job; only the receiver (Service→Engine) and host
// access (via ops/meta/now/metrics) change.
type Engine struct {
	ops     JobOps
	meta    *jobstore.Store
	now     func() time.Time
	metrics job.MetricsSink
}

// NewEngine binds the engine to a host JobOps (typically *job.Service). meta/metrics
// are cached (stable); now is the host clock (bound method value, stays live; tests
// in this package may override e.now directly).
func NewEngine(ops JobOps) *Engine {
	return &Engine{ops: ops, meta: ops.Meta(), now: ops.Now, metrics: ops.Metrics()}
}

var _ job.WorkflowAdvancer = (*Engine)(nil)
