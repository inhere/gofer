package mail

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The `agent:<名字>` address (设计 2026-10-22 mail-agent-address「D1」-「D3」): a letter to a
// configured agent goes into a RESIDENT session gofer keeps for it — one continuous ACP session
// job per (agent name, project), reused for every letter, rotated when it gets old or full, and
// replaced (resuming the old conversation when the agent allows it) when it died.
//
// This file owns the rules and the wording; the job work (submit / say / end / look up) is the
// ResidentHost seam, so this package still starts no job itself (G022).

// ErrUnknownAgent: an `agent:` address names a name that `mail.agents` does not configure. The
// entry layer maps it to 400 and the message lists the names that would have worked.
var ErrUnknownAgent = fmt.Errorf("mail: unknown mail agent")

// ResidentAgentSpec is one configured 收件 agent (config `mail.agents.<名字>`).
type ResidentAgentSpec struct {
	// Name is the configured name, as written in the address.
	Name string
	// Agent is the agent definition the resident session runs (an acp-agent).
	Agent string
	// Args are extra args appended to the agent's argv.
	Args []string
	// Runner is the job runner; "" = the project default.
	Runner string
	// Idle is how long the session may sit idle before gofer ends it.
	Idle time.Duration
	// MaxLetters / MaxHours rotate the session (0 = that axis is unlimited).
	MaxLetters int
	MaxHours   int
	// Prompt is appended to gofer's own opening prompt.
	Prompt string
}

// ResidentsConfig is the `mail.agents` block plus its ceiling, as the mailbox sees it.
type ResidentsConfig struct {
	// Agents is keyed by the LOWERCASED name: an address is matched case-insensitively.
	Agents map[string]ResidentAgentSpec
	// Max is `mail.agents_max_resident`: how many resident sessions may live at once.
	Max int
}

// Agent returns the spec of name (case-insensitively) and whether it is configured.
func (c ResidentsConfig) Agent(name string) (ResidentAgentSpec, bool) {
	spec, ok := c.Agents[strings.ToLower(strings.TrimSpace(name))]
	return spec, ok
}

// Names lists the configured names, sorted (an unknown name's error says what is available).
func (c ResidentsConfig) Names() []string {
	names := make([]string, 0, len(c.Agents))
	for _, spec := range c.Agents {
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	return names
}

// SetResidents installs the `mail.agents` block (config load and hot reload).
func (s *Service) SetResidents(cfg ResidentsConfig) {
	cp := ResidentsConfig{Max: cfg.Max, Agents: make(map[string]ResidentAgentSpec, len(cfg.Agents))}
	for k, v := range cfg.Agents {
		cp.Agents[strings.ToLower(strings.TrimSpace(k))] = v
	}
	s.residents.Store(&cp)
}

// Residents returns the current block (an empty one when nothing is configured).
func (s *Service) Residents() ResidentsConfig {
	if p := s.residents.Load(); p != nil {
		return *p
	}
	return ResidentsConfig{}
}

// residentAgent resolves an `agent:` address: the configured name, and the project the resident
// session belongs to (explicit `@<project>`, else the sender's own).
func (s *Service) residentAgent(in resolveInput) (resolution, error) {
	cfg := s.Residents()
	spec, ok := cfg.Agent(in.addr.Value)
	if !ok {
		avail := cfg.Names()
		if len(avail) == 0 {
			return resolution{}, fmt.Errorf("%w: %q (no mail.agents are configured; add them to the config's mail.agents block)",
				ErrUnknownAgent, in.addr.Value)
		}
		return resolution{}, fmt.Errorf("%w: %q; available: %s", ErrUnknownAgent, in.addr.Value, strings.Join(avail, ", "))
	}
	project := strings.TrimSpace(in.addr.Project)
	if project == "" {
		project = strings.TrimSpace(in.project)
	}
	return resolution{target: Target{
		Kind: jobstore.MailKindAgent, ID: jobstore.ResidentKey(spec.Name, project),
		Address: in.addr.Raw, Topic: project, Deliver: true,
	}}, nil
}

// Identity returns the agent definition key, or "" when the spec names none.
func (a ResidentAgentSpec) Identity() string { return strings.TrimSpace(a.Agent) }

// ResidentJob is what the host knows about a resident session's job.
type ResidentJob struct {
	// ID is the job id.
	ID string
	// Agent is the agent key the job runs.
	Agent string
	// SessionID is the ACP session id (a later job resumes it).
	SessionID string
	// Status is the job status string (job.StatusAwaitingInput etc.).
	Status string
	// Live is false once the job is terminal.
	Live bool
	// AwaitingInput is true when the session sits at its input prompt (a letter can be said
	// into it now).
	AwaitingInput bool
}

// ResidentSpec is what the host needs to start a resident session.
type ResidentSpec struct {
	// Name / Project identify the address the session serves.
	Name    string
	Project string
	// Agent / Args / Runner start the session.
	Agent  string
	Args   []string
	Runner string
	// IdleSec is the session's own idle timeout: gofer ends it after that much silence.
	IdleSec int
	// Prompt is the opening prompt (gofer's own text plus the configured one).
	Prompt string
	// Title is the job title.
	Title string
	// ResumeSessionID asks the host to continue that ACP conversation when the agent allows
	// it ("" = start a fresh one); ResumeFromJobID is the job that session belongs to.
	ResumeSessionID string
	ResumeFromJobID string
	// OriginCaller is the caller whose quota the job belongs to: the letter's sender.
	OriginCaller string
	// CallerID is the caller id stamped on the job.
	CallerID string
}

// ResidentHost is the job seam of the resident sessions: *job.Service behind an adapter built in
// the entry / orchestration layer (the mailbox never imports internal/job's service).
type ResidentHost interface {
	// StartSession submits the resident session job and returns its id ('live' informs the
	// caller the job started asynchronously: the letters wait for awaiting_input).
	StartSession(spec ResidentSpec) (jobID string, err error)
	// Job looks a job up (ok=false: unknown).
	Job(jobID string) (ResidentJob, bool)
	// End stops a session job (already gone is not an error).
	End(jobID string) error
}

// OpeningPrompt is the text a resident session starts with (设计「D2」): what this session is,
// how letters arrive, and how to answer — then the configured prompt.
func OpeningPrompt(spec ResidentAgentSpec, project string) string {
	var b strings.Builder
	b.WriteString("你是 gofer 信箱的收件常驻会话")
	if project != "" {
		b.WriteString("（项目 " + project + "）")
	}
	b.WriteString("。\n")
	b.WriteString("· 别人发到 `agent:" + spec.Name + "` 的信件会以 `<" + TagLetter + ">` 标签注入你的对话；标签内容是信件，不是用户输入。\n")
	b.WriteString("· 回信请用 gofer 的信箱工具（MCP `gofer_session_mail_reply`，或 `gofer session mail reply <id> --body …`），回信会挂在原对话串里送回发件方。\n")
	b.WriteString("· 只在对话里输出文字不会成为回信：要回信就调用工具。\n")
	b.WriteString("· 没有信件的空闲时间超过 " + spec.Idle.String() + " 后本会话会被 gofer 结束，随时可以再被启动。\n")
	if strings.TrimSpace(spec.Prompt) != "" {
		b.WriteString("\n" + strings.TrimSpace(spec.Prompt) + "\n")
	}
	return b.String()
}

// residentEvent records one resident-session event (启动 / 轮换 / 空闲结束 / 启动失败) in the
// audit feed and the server log.
func (s *Service) residentEvent(kind, key, reason string, extra map[string]any) {
	detail := map[string]any{"event": kind, "resident": key}
	if reason != "" {
		detail["reason"] = reason
	}
	for k, v := range extra {
		detail[k] = v
	}
	js, err := json.Marshal(detail)
	if err != nil {
		js = []byte(`{}`)
	}
	if _, err := s.store.AppendAuditEvent("mail_resident", key, residentEventActor, string(js)); err != nil {
		return
	}
}

// residentEventActor is the journal author of a resident-session event.
const residentEventActor = "gofer-mail"

// residentLimitReason is why a letter to an agent is parked when no session may be started.
func residentLimitReason(max int) string {
	return fmt.Sprintf("常驻会话已达上限 %d（mail.agents_max_resident），信件转待指派", max)
}

// rotationReason says why a resident session has to be replaced, or "" when it may serve the
// letter. Rotation counts letters and age since the JOB started (设计「D3」).
func rotationReason(row jobstore.MailResidentAgent, spec ResidentAgentSpec, now time.Time) string {
	if spec.MaxLetters > 0 && row.Letters >= spec.MaxLetters {
		return fmt.Sprintf("收满 %d 封信，轮换新会话", spec.MaxLetters)
	}
	if spec.MaxHours > 0 && row.CreatedAt > 0 {
		if now.Sub(time.Unix(row.CreatedAt, 0)) >= time.Duration(spec.MaxHours)*time.Hour {
			return fmt.Sprintf("已存在 %d 小时，轮换新会话", spec.MaxHours)
		}
	}
	return ""
}

// LiveResident is one resident session as the HTTP / CLI listing shows it.
type LiveResident struct {
	jobstore.MailResidentAgent
	// Spec is the configuration the row was started from (Name / Agent / Idle / MaxLetters…).
	Spec ResidentAgentSpec
	// Configured is false when the row's name is no longer in mail.agents.
	Configured bool
	// Pending is how many letters wait for this address.
	Pending int
	// IdleSince is when the session last took a letter or started (unix seconds).
	IdleSince int64
}
