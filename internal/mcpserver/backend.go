package mcpserver

import "github.com/inhere/gofer/internal/job"

// Backend abstracts the backend operations behind the 10 bridge_* MCP tools so
// the handlers can target either an in-process job.Service + registries
// (localBackend, the original standalone path) or a remote central serve via
// internal/client (clientBackend, P3). This is the seam introduced by E28: the
// handlers keep input validation (e.g. tail_log stream defaulting/checking) and
// view projection (jobView / interactionView / projectEntry …), while the
// Backend returns domain types (job.JobResult / job.Interaction) or — where the
// two ends' source shapes differ (projects / agents / artifacts) — the mcpserver
// view types directly. Signatures mirror design §7.
type Backend interface {
	ListProjects() ([]projectEntry, error)
	ListAgents() ([]agentEntry, error)
	RunJob(req job.JobRequest) (job.JobResult, error)
	GetJob(id string) (job.JobResult, error)
	TailLog(id, stream string, maxBytes int64) (string, error)
	CancelJob(id string) (job.JobResult, error)
	GetInteractions(id string) ([]job.Interaction, error)
	AnswerInteraction(id, iid, answer string) (job.Interaction, error)
	GetArtifacts(id string) ([]artifactView, error)
	GetResult(id string) (string, error)
}
