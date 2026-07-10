package mcpserver

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/presence"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/store"
)

// errPresenceUnavailable guards the presence tools when the local backend was
// wired without a presence service (e.g. a presence-less test fixture). In
// production the standalone path always wires one (core.Build).
var errPresenceUnavailable = errors.New("presence service not configured")

// localBackend is the in-process Backend: it operates directly on the shared
// registries + job.Service (the original standalone path). Every method holds
// the exact backend calls that used to live inside the gofer_* handler closures
// (E28 P2, G023 zero behavior change) — input validation and view projection
// stay in the handlers; only the backend access moved here.
type localBackend struct {
	jobs     *job.Service
	projects *project.Registry
	agents   *agent.Registry
	// presence backs the E36 gofer_* presence tools (nil in presence-less
	// fixtures; the presence handlers then return errPresenceUnavailable).
	presence *presence.Service
}

// newLocalBackend wires a localBackend over the same registries/job service the
// HTTP control plane uses. presence may be nil (the presence tools then error).
func newLocalBackend(jobs *job.Service, projects *project.Registry, agents *agent.Registry, pres *presence.Service) *localBackend {
	return &localBackend{jobs: jobs, projects: projects, agents: agents, presence: pres}
}

func (b *localBackend) ListProjects() ([]projectEntry, error) {
	keys := b.projects.List() // already sorted
	out := make([]projectEntry, 0, len(keys))
	for _, key := range keys {
		p, err := b.projects.Get(key)
		if err != nil {
			continue
		}
		out = append(out, projectEntry{
			Key:               key,
			HostPath:          p.HostPath,
			ContainerPath:     p.ContainerPath,
			DefaultAgent:      p.DefaultAgent,
			AllowedAgents:     p.AllowedAgents,
			AllowedRunners:    p.AllowedRunners,
			AllowExec:         p.AllowExec,
			MaxConcurrentJobs: p.MaxConcurrentJobs,
		})
	}
	return out, nil
}

func (b *localBackend) ListAgents() ([]agentEntry, error) {
	list := b.agents.List()
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]agentEntry, 0, len(keys))
	for _, k := range keys {
		ac := list[k]
		det := b.agents.Detect(k)
		// detail: version when available, else the captured probe error.
		detail := det.Version
		if !det.Available {
			detail = det.Error
		}
		out = append(out, agentEntry{
			Name:      k,
			Type:      ac.Type,
			Available: det.Available,
			Detail:    detail,
		})
	}
	return out, nil
}

func (b *localBackend) RunJob(req job.JobRequest) (job.JobResult, error) {
	return b.jobs.Submit(req)
}

func (b *localBackend) GetJob(id string) (job.JobResult, error) {
	res, ok := b.jobs.Get(id)
	if !ok {
		return job.JobResult{}, fmt.Errorf("unknown job %q", id)
	}
	return res, nil
}

func (b *localBackend) TailLog(id, stream string, maxBytes int64) (string, error) {
	data, err := b.jobs.TailLog(id, store.Stream(stream), maxBytes)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (b *localBackend) CancelJob(id string) (job.JobResult, error) {
	if err := b.jobs.Cancel(id); err != nil {
		// The only Cancel error is an unknown job id (terminal jobs are no-ops).
		return job.JobResult{}, err
	}
	res, _ := b.jobs.Get(id)
	return res, nil
}

func (b *localBackend) GetInteractions(id string) ([]job.Interaction, error) {
	return b.jobs.GetInteractions(id)
}

func (b *localBackend) AnswerInteraction(id, iid, answer, responder string) (job.Interaction, error) {
	return b.jobs.AnswerInteractionBy(id, iid, answer, responder)
}

func (b *localBackend) PuntInteraction(id, iid string) error {
	return b.jobs.MarkInteractionNeedsHuman(id, iid)
}

func (b *localBackend) ListPendingInteractions() ([]job.Interaction, error) {
	return b.jobs.ListPendingInteractions()
}

func (b *localBackend) GetArtifacts(id string) ([]artifactView, error) {
	// Manifest resolution (persisted ArtifactsJSON preferred, else a live scan)
	// is the shared job.Service.GetArtifactManifest — the same data-plane the HTTP
	// list endpoint uses. ok=false is an unknown job. Items is always non-nil.
	m, ok := b.jobs.GetArtifactManifest(id)
	if !ok {
		return nil, fmt.Errorf("unknown job %q", id)
	}
	out := make([]artifactView, 0, len(m.Items))
	for _, it := range m.Items {
		out = append(out, artifactView{Name: it.Name, Size: it.Size, Mtime: it.Mtime})
	}
	return out, nil
}

func (b *localBackend) GetResult(id string) (string, error) {
	res, ok := b.jobs.Get(id)
	if !ok {
		return "", fmt.Errorf("unknown job %q", id)
	}
	return res.ResultJSON, nil
}

// --- plan grouping (local 直驱 jobstore via Meta) ---------------------------

func newPlanID() string {
	return "plan-" + time.Now().Format(job.JobIDLayout) + "-" + job.RandomSuffix()
}

func (b *localBackend) CreatePlan(title, description string) (planView, error) {
	now := time.Now().Unix()
	p := jobstore.Plan{
		PlanID:      newPlanID(),
		Title:       title,
		Description: description,
		Status:      jobstore.PlanOpen,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := b.jobs.Meta().InsertPlan(p); err != nil {
		return planView{}, err
	}
	return planHeaderView(p), nil
}

func (b *localBackend) AttachJob(planID, jobID string) (planView, error) {
	st := b.jobs.Meta()
	p, ok, err := st.GetPlan(planID)
	if err != nil {
		return planView{}, err
	}
	if !ok {
		return planView{}, fmt.Errorf("unknown plan %q", planID)
	}
	attached, err := st.AttachJobToPlan(jobID, planID)
	if err != nil {
		return planView{}, err
	}
	if !attached {
		return planView{}, fmt.Errorf("unknown job %q", jobID)
	}
	_ = st.TouchPlan(planID)
	p, _, _ = st.GetPlan(planID)
	return planHeaderView(p), nil
}

func (b *localBackend) GetPlan(planID string) (planView, error) {
	st := b.jobs.Meta()
	p, ok, err := st.GetPlan(planID)
	if err != nil {
		return planView{}, err
	}
	if !ok {
		return planView{}, fmt.Errorf("unknown plan %q", planID)
	}
	jobs, err := b.jobs.ListJobs(job.ListOpts{Plan: planID, Limit: 1000})
	if err != nil {
		return planView{}, err
	}
	raw, err := st.PlanJobStatusCounts(planID)
	if err != nil {
		return planView{}, err
	}
	pv := planHeaderView(p)
	pv.Counts = jobstore.RollupPlanCounts(raw)
	pv.Jobs = make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		pv.Jobs = append(pv.Jobs, toJobView(j))
	}
	return pv, nil
}

// --- E36 presence (local 直驱 presence.Service) ------------------------------

// RegisterAgent registers this agent in-process. Provenance: no auth caller in
// standalone mode (caller_id stays ""), Client is the MCP server host name.
func (b *localBackend) RegisterAgent(name, role, project string) (presence.RegisterResult, error) {
	if b.presence == nil {
		return presence.RegisterResult{}, errPresenceUnavailable
	}
	return b.presence.Register(presence.RegisterInput{
		Name:       name,
		Role:       role,
		ProjectKey: project,
		Client:     mcpHostname(),
	})
}

func (b *localBackend) PollInbox(agentID, token string, ack bool) ([]presence.Message, error) {
	if b.presence == nil {
		return nil, errPresenceUnavailable
	}
	return b.presence.Poll(agentID, token, ack)
}

func (b *localBackend) PostMessage(from, to, kind, body, ref string) (int, error) {
	if b.presence == nil {
		return 0, errPresenceUnavailable
	}
	return b.presence.Post(from, to, kind, body, ref)
}

func (b *localBackend) ListPresence(role, project string) ([]presence.Agent, error) {
	if b.presence == nil {
		return nil, errPresenceUnavailable
	}
	return b.presence.List(role, project)
}
