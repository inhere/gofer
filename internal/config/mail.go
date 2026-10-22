package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MailConfig is the top-level `mail:` block: the gofer 信箱 server deliverer's numbers
// (design 2026-10-16「默认数值」, plan M5). Every key is optional; 0 / unset takes the
// documented default, and a negative value is a load error. The deliverer reads them at
// each reload, so an edit applies without a restart (the tick interval from the sweep after
// the one in flight).
type MailConfig struct {
	// TickSec is how often the deliverer sweeps (release expired leases, park overdue
	// letters, wake idle sessions, deliver to suga / ACP jobs). Default 30.
	TickSec int `yaml:"tick_sec,omitempty"`
	// WakeIdleSec is how long a session must have been silent (last_seen_at) before an
	// idle / offline session with pending letters is woken. Default 300 (5 minutes).
	WakeIdleSec int `yaml:"wake_idle_sec,omitempty"`
	// WakeIntervalSec is the minimum gap between two wakes of one session. Default 600.
	WakeIntervalSec int `yaml:"wake_interval_sec,omitempty"`
	// DeliverBySec is the delivery deadline of NEW letters: past it an undelivered letter
	// goes to the capture box. Default 86400 (24 hours).
	DeliverBySec int `yaml:"deliver_by_sec,omitempty"`
	// ClaimLeaseSec is the lease of the deliverer's claims (suga send / job say): a claim not
	// confirmed within it returns to pending. Default 60. Hook claims keep the 60 s default.
	ClaimLeaseSec int `yaml:"claim_lease_sec,omitempty"`
	// BroadcastMaxRecipients is 单次广播最多收件人数 (设计 2026-10-22「D6」). Default 20.
	BroadcastMaxRecipients int `yaml:"broadcast_max_recipients,omitempty"`
	// BroadcastRate is 广播频率, "<count>/<window>" (default "5/10m"): each caller may send
	// that many broadcasts per window, whatever the recipient count.
	BroadcastRate string `yaml:"broadcast_rate,omitempty"`
	// Agents is 哪些 agent 能收信 (设计 2026-10-22 mail-agent-address「D2」): the key is the name an
	// `agent:<name>` address uses, the value is how its resident session is started.
	Agents map[string]MailAgentConfig `yaml:"agents,omitempty"`
	// AgentsMaxResident is 全局最多同时存在的常驻会话 (default 6; 0 = default).
	AgentsMaxResident int `yaml:"agents_max_resident,omitempty"`
}

// MailAgentConfig is one 收件 agent of the `mail.agents.<名字>` block.
//
// The resident session is an ACP continuous session job (`--session`), so the agent it names
// has to be an acp-agent; anything else cannot be sent a letter programmatically.
type MailAgentConfig struct {
	// Agent is the agent definition the resident session runs (`acp-agent` only).
	Agent string `yaml:"agent,omitempty"`
	// Args are extra args appended to the agent's own argv.
	Args []string `yaml:"args,omitempty"`
	// Runner is the job runner; empty = the project's default runner.
	Runner string `yaml:"runner,omitempty"`
	// IdleMinutes ends the session after that much idle time (default 120; 0 = default).
	IdleMinutes int `yaml:"idle_minutes,omitempty"`
	// MaxLetters rotates the session after that many letters (default 50; 0 = 不限).
	MaxLetters *int `yaml:"max_letters,omitempty"`
	// MaxHours rotates the session after that many hours (default 24; 0 = 不限).
	MaxHours *int `yaml:"max_hours,omitempty"`
	// Prompt is appended to gofer's own opening prompt (optional).
	Prompt string `yaml:"prompt,omitempty"`
	// Approval is the ACP approval gate of THIS resident session (设计 2026-10-22
	// mail-agent-address「D4」), same structure and validation as the project's `approval`:
	// e.g. `approval: {mode: ask}` makes the 收件会话 ask a human before writing files or
	// running commands while every other ACP session of the project keeps the project's
	// policy. Unset = the project's policy (unchanged behaviour).
	Approval *ApprovalConfig `yaml:"approval,omitempty"`
}

// Mail agent defaults.
const (
	DefaultMailAgentIdleMinutes  = 120
	DefaultMailAgentMaxLetters   = 50
	DefaultMailAgentMaxHours     = 24
	DefaultMailAgentsMaxResident = 6
)

// Identity returns the agent definition key, or "" when the block names none.
func (a MailAgentConfig) Identity() string { return strings.TrimSpace(a.Agent) }

// EffectiveIdle is idle_minutes with the default applied.
func (a MailAgentConfig) EffectiveIdle() time.Duration {
	m := a.IdleMinutes
	if m <= 0 {
		m = DefaultMailAgentIdleMinutes
	}
	return time.Duration(m) * time.Minute
}

// EffectiveMaxLetters is max_letters (a nil member = default; an explicit 0 = unlimited).
func (a MailAgentConfig) EffectiveMaxLetters() int {
	if a.MaxLetters == nil {
		return DefaultMailAgentMaxLetters
	}
	return *a.MaxLetters
}

// EffectiveMaxHours is max_hours (a nil member = default; an explicit 0 = unlimited).
func (a MailAgentConfig) EffectiveMaxHours() int {
	if a.MaxHours == nil {
		return DefaultMailAgentMaxHours
	}
	return *a.MaxHours
}

// Mail defaults (seconds), and the broadcast numbers.
const (
	DefaultMailTickSec                = 30
	DefaultMailWakeIdleSec            = 300
	DefaultMailWakeIntervalSec        = 600
	DefaultMailDeliverBySec           = 24 * 3600
	DefaultMailClaimLeaseSec          = 60
	DefaultMailBroadcastMaxRecipients = 20
	DefaultMailBroadcastRate          = "5/10m"
)

// MailSettings is MailConfig resolved to durations.
type MailSettings struct {
	Tick         time.Duration
	WakeIdle     time.Duration
	WakeInterval time.Duration
	DeliverBy    time.Duration
	Lease        time.Duration
	// BroadcastMax / BroadcastRate / BroadcastWindow are the broadcast limits (D6).
	BroadcastMax    int
	BroadcastRate   int
	BroadcastWindow time.Duration
}

// EffectiveMail resolves the `mail:` block with the defaults applied.
func (c *Config) EffectiveMail() MailSettings {
	var m MailConfig
	if c != nil {
		m = c.Mail
	}
	pick := func(v, def int) time.Duration {
		if v <= 0 {
			v = def
		}
		return time.Duration(v) * time.Second
	}
	// A malformed rate was already refused by validate; here it falls back so a programmatic
	// config (tests, a caller that skipped validation) still has a working ceiling.
	rate, window, err := ParseBroadcastRate(m.BroadcastRate)
	if err != nil {
		rate, window, _ = ParseBroadcastRate(DefaultMailBroadcastRate)
	}
	return MailSettings{
		Tick:         pick(m.TickSec, DefaultMailTickSec),
		WakeIdle:     pick(m.WakeIdleSec, DefaultMailWakeIdleSec),
		WakeInterval: pick(m.WakeIntervalSec, DefaultMailWakeIntervalSec),
		DeliverBy:    pick(m.DeliverBySec, DefaultMailDeliverBySec),
		Lease:        pick(m.ClaimLeaseSec, DefaultMailClaimLeaseSec),

		BroadcastMax:    pickInt(m.BroadcastMaxRecipients, DefaultMailBroadcastMaxRecipients),
		BroadcastRate:   rate,
		BroadcastWindow: window,
	}
}

// ParseBroadcastRate parses "<count>/<window>" ("5/10m"): the broadcast ceiling of one caller.
// A malformed value is a load error, never silently defaulted — a limit nobody can read is a
// limit nobody can rely on.
func ParseBroadcastRate(v string) (count int, window time.Duration, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		v = DefaultMailBroadcastRate
	}
	n, w, ok := strings.Cut(v, "/")
	if !ok {
		return 0, 0, fmt.Errorf("mail.broadcast_rate %q: want <count>/<window>, e.g. 5/10m", v)
	}
	count, err = strconv.Atoi(strings.TrimSpace(n))
	if err != nil || count <= 0 {
		return 0, 0, fmt.Errorf("mail.broadcast_rate %q: count must be a positive integer", v)
	}
	window, err = time.ParseDuration(strings.TrimSpace(w))
	if err != nil || window <= 0 {
		return 0, 0, fmt.Errorf("mail.broadcast_rate %q: window must be a positive duration", v)
	}
	return count, window, nil
}

func pickInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// EffectiveAgentsMaxResident is agents_max_resident with the default applied.
func (m MailConfig) EffectiveAgentsMaxResident() int {
	return pickInt(m.AgentsMaxResident, DefaultMailAgentsMaxResident)
}

// Agent returns one 收件 agent by name (case-insensitively, like a runner name) and whether it
// is configured.
func (m MailConfig) Agent(name string) (MailAgentConfig, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return MailAgentConfig{}, false
	}
	for k, v := range m.Agents {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return MailAgentConfig{}, false
}

// AgentNames lists the configured 收件 agent names, sorted, so an error can say what is可用.
func (m MailConfig) AgentNames() []string {
	names := make([]string, 0, len(m.Agents))
	for k := range m.Agents {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (m MailConfig) validate() error {
	if _, _, err := ParseBroadcastRate(m.BroadcastRate); err != nil {
		return err
	}
	for _, f := range []struct {
		key string
		v   int
	}{
		{"tick_sec", m.TickSec}, {"wake_idle_sec", m.WakeIdleSec}, {"wake_interval_sec", m.WakeIntervalSec},
		{"deliver_by_sec", m.DeliverBySec}, {"claim_lease_sec", m.ClaimLeaseSec},
		{"broadcast_max_recipients", m.BroadcastMaxRecipients}, {"agents_max_resident", m.AgentsMaxResident},
	} {
		if f.v < 0 {
			return fmt.Errorf("mail.%s must be >= 0 (0 = default)", f.key)
		}
	}
	for name, a := range m.Agents {
		key := "mail.agents." + name
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("mail.agents: an agent name cannot be blank")
		}
		if strings.ContainsAny(name, " \t@:") {
			return fmt.Errorf("%s: an agent name cannot contain a space, '@' or ':' (it is written agent:<名字>) yet %q does", key, name)
		}
		if a.Identity() == "" {
			return fmt.Errorf("%s.agent is required (the acp-agent to start)", key)
		}
		if a.IdleMinutes < 0 {
			return fmt.Errorf("%s.idle_minutes must be >= 0 (0 = default)", key)
		}
		if a.MaxLetters != nil && *a.MaxLetters < 0 {
			return fmt.Errorf("%s.max_letters must be >= 0 (0 = unlimited)", key)
		}
		if a.MaxHours != nil && *a.MaxHours < 0 {
			return fmt.Errorf("%s.max_hours must be >= 0 (0 = unlimited)", key)
		}
		if a.Approval != nil {
			if err := validateApprovalAt(key, *a.Approval); err != nil {
				return err
			}
		}
	}
	return nil
}
