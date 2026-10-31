package mail

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Delivering a letter to an `agent:` address (设计 2026-10-22 mail-agent-address「D3」): find the
// resident session of that (name, project), reuse it when it is alive, rotate it when it is full
// or old, start it when there is none, and park the letter when no session may be started. The
// start and the delivery are both asynchronous: the send call only stores the letter.

const (
	// residentStartRetry is how long a failed start is left alone before it is tried again, so a
	// broken agent definition costs one attempt per interval instead of one per sweep.
	residentStartRetry = 2 * time.Minute
)

// sweepAgents delivers the pending `agent:` letters, address by address.
func (d *Deliverer) sweepAgents(ctx context.Context, now time.Time, st Settings) {
	ids, byID, err := d.pendingBy(jobstore.MailKindAgent)
	if err != nil {
		slog.Warn("mail deliverer: list pending agent mail failed", "error", err)
		return
	}
	for _, key := range ids {
		if ctx.Err() != nil {
			return
		}
		d.deliverToAgent(ctx, key, byID[key], now, st)
	}
}

// deliverToAgent serves the letters of one `agent:<名字>@<项目>` address.
func (d *Deliverer) deliverToAgent(ctx context.Context, key string, letters []jobstore.Mail, now time.Time, st Settings) {
	host := d.svc.residentHost
	if host == nil {
		d.holdAgentLetters(letters, "this server does not start resident sessions")
		return
	}
	name, project := jobstore.SplitResidentKey(key)
	spec, configured := d.svc.Residents().Agent(name)
	if !configured {
		d.holdAgentLetters(letters, i18n.T(d.svc.textLanguage(), "mail.reason.agent_unknown", "name", fmt.Sprintf("%q", name), "key", key))
		return
	}
	if spec.Identity() == "" {
		d.holdAgentLetters(letters, i18n.T(d.svc.textLanguage(), "mail.reason.agent_unset", "name", spec.Name))
		return
	}
	row, hasRow, err := d.svc.store.GetResidentAgent(spec.Name, project)
	if err != nil {
		slog.Warn("mail deliverer: read resident agent failed", "resident", key, "error", err)
		return
	}
	// A live row whose job still runs may serve the letters.
	if hasRow && row.Status == jobstore.MailResidentLive && row.JobID != "" {
		job, found := host.Job(row.JobID)
		if found && job.Live {
			// The ACP session id of a fresh job is only known once the job has started, so the
			// row learns it on a later sweep (a start that dies before it gets one can only start
			// the conversation over).
			if row.SessionID == "" && job.SessionID != "" {
				row.SessionID = job.SessionID
				if err := d.svc.store.PutResidentAgent(row); err != nil {
					slog.Warn("mail deliverer: recording the resident session id failed", "resident", key, "error", err)
				}
			}
			if why := rotationReason(d.svc.textLanguage(), row, spec, now); why != "" {
				d.rotateResident(row, spec, why)
				d.startResident(ctx, key, project, spec, "", "", d.letterOrigin(letters[0]), letters[0], now)
				return
			}
			if job.AwaitingInput {
				d.sayResident(ctx, key, job.ID, letters[0], st)
				return
			}
			return // running a turn: the letter waits for awaiting_input
		}
		// The session died (idle timeout, crash, restart): note it, then carry the conversation
		// over to a new job when the agent allows it.
		if err := d.svc.store.EndResidentAgent(spec.Name, project, jobstore.MailResidentEnded, i18n.T(d.svc.textLanguage(), "mail.reason.session_ended")); err != nil {
			slog.Warn("mail deliverer: end resident agent failed", "resident", key, "error", err)
		}
		d.svc.residentEvent("ended", key, i18n.T(d.svc.textLanguage(), "mail.reason.session_ended"), map[string]any{"job": row.JobID})
	}
	// A start that just failed keeps the letters pending until its retry window passes.
	if hasRow && row.Status == jobstore.MailResidentFailed && row.EndedAt > 0 &&
		now.Sub(time.Unix(row.EndedAt, 0)) < residentStartRetry {
		return
	}
	if max := d.svc.Residents().Max; max > 0 {
		n, err := d.svc.store.CountLiveResidentAgents()
		if err != nil {
			slog.Warn("mail deliverer: count resident agents failed", "error", err)
			return
		}
		if n >= max {
			d.holdAgentLetters(letters, residentLimitReason(d.svc.textLanguage(), max))
			return
		}
	}
	resume, resumeFrom := "", ""
	if hasRow {
		resume, resumeFrom = row.SessionID, row.JobID
	}
	d.startResident(ctx, key, project, spec, resume, resumeFrom, d.letterOrigin(letters[0]), letters[0], now)
}

// startResident submits the resident session job and records the row (设计「D3」2-3). The
// letters are not delivered here: the session needs a moment to reach awaiting_input, and the
// next sweep delivers them.
func (d *Deliverer) startResident(ctx context.Context, key, project string, spec ResidentAgentSpec, resume, resumeFrom, originCaller string, m jobstore.Mail, now time.Time) {
	_ = ctx
	host := d.svc.residentHost
	specPrompt := OpeningPrompt(d.svc.textLanguage(), spec, project)
	idle := int(spec.Idle / time.Second)
	if idle <= 0 {
		idle = 1800
	}
	jobID, err := host.StartSession(ResidentSpec{
		Name: spec.Name, Project: project, Agent: spec.Identity(), Args: spec.Args, Runner: spec.Runner,
		IdleSec: idle, Prompt: specPrompt, Title: "mail agent " + spec.Name,
		ResumeSessionID: resume, ResumeFromJobID: resumeFrom, OriginCaller: originCaller, CallerID: originCaller,
		Approval: spec.Approval,
	})
	if err != nil {
		slog.Warn("mail deliverer: starting a resident session failed", "resident", key, "error", err)
		if werr := d.svc.store.PutResidentAgent(jobstore.MailResidentAgent{
			Name: spec.Name, Project: project, JobID: "", SessionID: resume, CreatedAt: now.Unix(),
			EndedAt: now.Unix(), Status: jobstore.MailResidentFailed, LastError: err.Error(),
		}); werr != nil {
			slog.Warn("mail deliverer: recording the failed start failed", "resident", key, "error", werr)
		}
		d.svc.residentEvent("start_failed", key, err.Error(), map[string]any{"agent": spec.Identity()})
		return
	}
	if err := d.svc.store.PutResidentAgent(jobstore.MailResidentAgent{
		Name: spec.Name, Project: project, JobID: jobID, SessionID: d.agentSessionID(jobID), CreatedAt: now.Unix(),
		Status: jobstore.MailResidentLive,
	}); err != nil {
		slog.Warn("mail deliverer: recording the resident session failed", "resident", key, "error", err)
	}
	d.svc.residentEvent("started", key, "", map[string]any{"job": jobID, "agent": spec.Identity(), "letter": m.ID})
}

// agentSessionID is the ACP session id of a resident job ("" while it has none yet).
func (d *Deliverer) agentSessionID(jobID string) string {
	if job, ok := d.svc.residentHost.Job(jobID); ok {
		return job.SessionID
	}
	return ""
}

// rotateResident ends a session that reached its letter / age ceiling; the caller starts its
// replacement without resuming (设计「已定」2: 轮换开新会话).
func (d *Deliverer) rotateResident(row jobstore.MailResidentAgent, spec ResidentAgentSpec, why string) {
	host := d.svc.residentHost
	if host != nil && row.JobID != "" {
		if err := host.End(row.JobID); err != nil {
			slog.Info("mail deliverer: ending a rotated session failed", "resident", jobstore.ResidentKey(spec.Name, row.Project), "error", err)
		}
	}
	if err := d.svc.store.EndResidentAgent(spec.Name, row.Project, jobstore.MailResidentEnded, why); err != nil {
		slog.Warn("mail deliverer: recording a rotation failed", "resident", jobstore.ResidentKey(spec.Name, row.Project), "error", err)
	}
	slog.Info("mail deliverer: rotating resident session", "resident", jobstore.ResidentKey(spec.Name, row.Project), "reason", why)
	d.svc.residentEvent("rotated", jobstore.ResidentKey(spec.Name, row.Project), why, map[string]any{"job": row.JobID})
}

// sayResident delivers ONE letter into the resident session (the job accepts one pending input
// at a time): claim → say → confirm, then count the letter towards rotation.
func (d *Deliverer) sayResident(ctx context.Context, key, jobID string, m jobstore.Mail, st Settings) {
	if d.jobs == nil {
		return
	}
	store := d.svc.store
	claimed, ok, err := store.ClaimMail(m.ID, jobstore.MailDelivererClaimer, st.Lease)
	if err != nil || !ok {
		return
	}
	if err := d.jobs.SaySession(jobID, d.renderFor(claimed)); err != nil {
		if _, _, rerr := store.ReleaseMailClaim(m.ID, jobstore.MailDelivererClaimer, "job say: "+err.Error()); rerr != nil {
			slog.Warn("mail deliverer: release claim failed", "mail", m.ID, "error", rerr)
		}
		return
	}
	if _, _, err := store.ConfirmMailClaim(m.ID, jobstore.MailDelivererClaimer); err != nil {
		slog.Warn("mail deliverer: confirm failed (the letter may be sent again)", "mail", m.ID, "error", err)
	}
	name, project := jobstore.SplitResidentKey(key)
	row, hasRow, err := store.GetResidentAgent(name, project)
	if err == nil && hasRow {
		if err := store.SetResidentLetters(name, project, row.Letters+1); err != nil {
			slog.Warn("mail deliverer: counting a resident letter failed", "resident", key, "error", err)
		}
	}
}

// holdAgentLetters parks letters of an agent address in the capture box with the reason
// (设计「D3」4: 上限 / 未配置 → 待指派，并说明原因).
func (d *Deliverer) holdAgentLetters(letters []jobstore.Mail, reason string) {
	for _, m := range letters {
		if _, _, err := d.svc.HoldLetter(m.ID, reason); err != nil {
			slog.Warn("mail deliverer: holding an agent letter failed", "mail", m.ID, "error", err)
		}
	}
}

// SetResidentHost installs the job seam of the resident sessions (the entry layer's adapter).
func (s *Service) SetResidentHost(h ResidentHost) { s.residentHost = h }

// ListResidents is the `agent:` listing (GET /v1/mail/agents, `gofer session mail agents`): the
// configured names plus the resident sessions that exist right now.
func (s *Service) ListResidents() ([]LiveResident, error) {
	rows, err := s.store.ListResidentAgents()
	if err != nil {
		return nil, err
	}
	cfg := s.Residents()
	out := make([]LiveResident, 0, len(rows))
	for _, row := range rows {
		spec, ok := cfg.Agent(row.Name)
		lr := LiveResident{MailResidentAgent: row, Spec: spec, Configured: ok, IdleSince: row.CreatedAt}
		if n, err := s.store.PendingCount(jobstore.MailKindAgent, jobstore.ResidentKey(row.Name, row.Project)); err == nil {
			lr.Pending = n
		}
		if s.residentHost != nil && row.JobID != "" {
			if job, found := s.residentHost.Job(row.JobID); found {
				if job.AwaitingInput {
					lr.IdleSince = s.now().Unix()
				}
				if !job.Live && row.Status == jobstore.MailResidentLive {
					lr.Status = jobstore.MailResidentEnded
				}
			}
		}
		out = append(out, lr)
	}
	return out, nil
}

// EndResident stops the resident session of one address by hand (POST /v1/mail/agents/{name}/end).
// It is not an error when the address has no live session: the row is marked ended either way so
// the listing stops showing it.
func (s *Service) EndResident(name, project, reason string) (string, error) {
	name, project = strings.TrimSpace(name), strings.TrimSpace(project)
	if name == "" {
		return "", fmt.Errorf("%w: agent name required", ErrInvalidAddress)
	}
	spec, configured := s.Residents().Agent(name)
	key := jobstore.ResidentKey(name, project)
	row, hasRow, err := s.store.GetResidentAgent(name, project)
	if err != nil {
		return "", err
	}
	if !hasRow {
		return "", fmt.Errorf("%w: %s has no resident session", ErrAgentNotResident, key)
	}
	jobID := row.JobID
	if s.residentHost != nil && jobID != "" && row.Status == jobstore.MailResidentLive {
		if err := s.residentHost.End(jobID); err != nil {
			return jobID, err
		}
	}
	if reason == "" {
		reason = i18n.T(s.textLanguage(), "mail.reason.manual_end")
	}
	if err := s.store.EndResidentAgent(name, project, jobstore.MailResidentEnded, reason); err != nil {
		return jobID, err
	}
	if configured {
		name = spec.Name
	}
	s.residentEvent("ended", jobstore.ResidentKey(name, project), reason, map[string]any{"job": jobID})
	return jobID, nil
}

// ErrAgentNotResident: the address has no resident session row, so there is nothing to end.
var ErrAgentNotResident = fmt.Errorf("mail: no resident session for that agent")
