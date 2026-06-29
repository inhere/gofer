package mcpserver

import (
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/presence"
)

// Backend abstracts the backend operations behind the 10 gofer_* MCP tools so
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
	// ListPendingInteractions lists pending interactions across active jobs (E25
	// supervisor discovery, gofer_list_pending_interactions).
	ListPendingInteractions() ([]job.Interaction, error)
	GetArtifacts(id string) ([]artifactView, error)
	GetResult(id string) (string, error)

	// E36 driver-agent identity/mailbox (4 of the 5 gofer_* presence tools;
	// list_pending_interactions is P3). local 直驱 presence.Service; client 转发
	// 中央 serve。返回 presence 域类型，handler 投影成 snake_case view。
	RegisterAgent(name, role, project string) (presence.RegisterResult, error)
	PollInbox(agentID, token string, ack bool) ([]presence.Message, error)
	PostMessage(from, to, kind, body, ref string) (int, error)
	ListPresence(role, project string) ([]presence.Agent, error)
}
