package mcpserver

import (
	"encoding/json"
	"fmt"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/presence"
)

// clientBackend is the remote Backend: every method forwards to a central gofer
// serve over the HTTP client (internal/client). It is the counterpart to
// localBackend (E28 P3) — the gofer_* handlers keep input validation + view
// projection, while this backend turns each call into a /v1/... request. The
// project/agent/artifact views are produced here to be byte-for-byte shape
// compatible with localBackend (same non-nil empty slices, same field mapping)
// so client mode and standalone mode surface identical tool output.
type clientBackend struct {
	cli *client.Client
}

// NewClientBackend wires a Backend that forwards to the central serve at cli.
func NewClientBackend(cli *client.Client) Backend {
	return &clientBackend{cli: cli}
}

// RunJob submits asynchronously and returns the initial (queued/running)
// snapshot, matching localBackend.RunJob's submit semantics.
func (b *clientBackend) RunJob(req job.JobRequest) (job.JobResult, error) {
	return b.cli.SubmitJob(req)
}

func (b *clientBackend) GetJob(id string) (job.JobResult, error) {
	return b.cli.GetJob(id)
}

func (b *clientBackend) CancelJob(id string) (job.JobResult, error) {
	return b.cli.CancelJob(id)
}

// GetResult returns the job's result.json content (the get_job snapshot's
// ResultJSON), mirroring localBackend.GetResult.
func (b *clientBackend) GetResult(id string) (string, error) {
	r, err := b.cli.GetJob(id)
	if err != nil {
		return "", err
	}
	return r.ResultJSON, nil
}

func (b *clientBackend) GetInteractions(id string) ([]job.Interaction, error) {
	return b.cli.GetInteractions(id)
}

func (b *clientBackend) AnswerInteraction(id, iid, answer, responder string) (job.Interaction, error) {
	// Forward the responder (this client's self-registered driver agent_id) so the central
	// serve's answer闸 (P3.1) grades the source server-side (presence/whitelist live there).
	return b.cli.AnswerInteraction(id, iid, answer, responder)
}

func (b *clientBackend) PuntInteraction(id, iid string) error {
	return b.cli.PuntInteraction(id, iid)
}

func (b *clientBackend) ListPendingInteractions() ([]job.Interaction, error) {
	return b.cli.ListPendingInteractions()
}

// TailLog reads the full stream from the server then trims to the last maxBytes
// client-side (the /logs endpoint returns the whole tail; the byte cap is the
// handler's contract). maxBytes<=0 means "no cap".
func (b *clientBackend) TailLog(id, stream string, maxBytes int64) (string, error) {
	s, err := b.cli.GetLogs(id, stream)
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && int64(len(s)) > maxBytes {
		s = s[int64(len(s))-maxBytes:]
	}
	return s, nil
}

// ListProjects maps the server's project meta into projectEntry. host_path /
// container_path are left empty because the meta endpoint does not expose
// server-side filesystem paths (same as `project list --remote`, E38); AllowExec
// / MaxConcurrentJobs are likewise absent from the meta shape, so they stay zero.
// The slice is always non-nil (matching localBackend).
func (b *clientBackend) ListProjects() ([]projectEntry, error) {
	metas, err := b.cli.ListProjects()
	if err != nil {
		return nil, err
	}
	out := make([]projectEntry, 0, len(metas))
	for _, m := range metas {
		out = append(out, projectEntry{
			Key:            m.Key,
			DefaultAgent:   m.DefaultAgent,
			AllowedAgents:  m.AllowedAgents,
			AllowedRunners: m.AllowedRunners,
		})
	}
	return out, nil
}

// ListAgents maps the server's agent meta (already folded to name/type/available/
// detail by client.ListAgents) 1:1 into agentEntry. Non-nil empty slice matches
// localBackend.
func (b *clientBackend) ListAgents() ([]agentEntry, error) {
	metas, err := b.cli.ListAgents()
	if err != nil {
		return nil, err
	}
	out := make([]agentEntry, 0, len(metas))
	for _, m := range metas {
		out = append(out, agentEntry{
			Name:      m.Name,
			Type:      m.Type,
			Available: m.Available,
			Detail:    m.Detail,
		})
	}
	return out, nil
}

// GetArtifacts fetches the peer job's manifest. client.ListArtifacts returns the
// inner `[{name,size,mtime},...]` array (unwrapped from {"artifacts":[...]}) as
// raw JSON; parse it into the artifactView projection. An empty/absent manifest
// (nil/zero-length raw) yields a non-nil empty slice, matching localBackend.
func (b *clientBackend) GetArtifacts(id string) ([]artifactView, error) {
	raw, err := b.cli.ListArtifacts(id)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return make([]artifactView, 0), nil
	}
	var items []job.ArtifactItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode artifacts manifest: %w", err)
	}
	out := make([]artifactView, 0, len(items))
	for _, it := range items {
		out = append(out, artifactView{Name: it.Name, Size: it.Size, Mtime: it.Mtime})
	}
	return out, nil
}

// --- plan grouping (client 转发中央 serve) -----------------------------------

func (b *clientBackend) CreatePlan(title, description string) (planView, error) {
	p, err := b.cli.CreatePlan("", title, description)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) AttachJob(planID, jobID string) (planView, error) {
	p, err := b.cli.AttachJob(planID, jobID)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func (b *clientBackend) GetPlan(planID string) (planView, error) {
	p, err := b.cli.GetPlan(planID)
	if err != nil {
		return planView{}, err
	}
	return clientPlanToView(p), nil
}

func clientPlanToView(p client.Plan) planView {
	pv := planView{
		PlanID:      p.PlanID,
		Title:       p.Title,
		Description: p.Description,
		Status:      p.Status,
		Owner:       p.Owner,
		Progress:    p.Progress,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Jobs:        make([]jobView, 0, len(p.Jobs)),
	}
	if p.Counts != nil {
		pv.Counts = *p.Counts
	}
	for _, j := range p.Jobs {
		pv.Jobs = append(pv.Jobs, toJobView(j))
	}
	return pv
}

// --- E36 presence (client 转发中央 serve) ------------------------------------

func (b *clientBackend) RegisterAgent(name, role, project string) (presence.RegisterResult, error) {
	id, tok, err := b.cli.RegisterAgent(name, role, project)
	if err != nil {
		return presence.RegisterResult{}, err
	}
	return presence.RegisterResult{AgentID: id, AgentToken: tok}, nil
}

func (b *clientBackend) PollInbox(agentID, token string, ack bool) ([]presence.Message, error) {
	return b.cli.PollInbox(agentID, token, ack)
}

func (b *clientBackend) PostMessage(from, to, kind, body, ref string) (int, error) {
	return b.cli.PostMessage(from, to, kind, body, ref)
}

func (b *clientBackend) ListPresence(role, project string) ([]presence.Agent, error) {
	return b.cli.ListPresence(role, project)
}
