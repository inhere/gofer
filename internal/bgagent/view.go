package bgagent

import (
	"time"

	"github.com/inhere/gofer/internal/runner"
)

// ViewVersion is the version of the wire view of the background-agent status (GET
// /v1/background-agents). A reader treats fields it does not know as additive and ignores
// them; the version only changes when an existing field changes meaning.
const ViewVersion = 1

// Kinds lists the background agent kinds in the order the status view shows them.
func Kinds() []string { return []string{KindSummarizer, KindSteward, KindJobCheck} }

// KnownKind reports whether kind is one of Kinds().
func KnownKind(kind string) bool {
	for _, k := range Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// View is the wire form of the status of every kind.
type View struct {
	Version int        `json:"version"`
	Kinds   []KindView `json:"kinds"`
}

// KindView is one kind: its active sessions (an empty list when there are none), today's
// totals and the rotation policy that applies to it.
type KindView struct {
	Kind     string        `json:"kind"`
	Sessions []SessionView `json:"sessions"`
	Today    TodayView     `json:"today"`
	Policy   PolicyView    `json:"policy"`
}

// SessionView is one active session. A context figure the agent never reported is null
// (not 0): ContextUsedTokens / ContextLimitTokens / ContextRatio are all nil together or
// individually when unknown.
type SessionView struct {
	ID                 string        `json:"id"`
	LastJobID          string        `json:"last_job_id,omitempty"`
	Agent              string        `json:"agent,omitempty"`
	Project            string        `json:"project,omitempty"`
	WorkDir            string        `json:"work_dir,omitempty"`
	Model              string        `json:"model,omitempty"`
	Runs               int           `json:"runs"`
	CreatedAt          int64         `json:"created_at"`
	LastUsedAt         int64         `json:"last_used_at"`
	ContextUsedTokens  *int64        `json:"context_used_tokens"`
	ContextLimitTokens *int64        `json:"context_limit_tokens"`
	ContextRatio       *float64      `json:"context_ratio"`
	RotationReason     string        `json:"rotation_reason,omitempty"`
	LastUsage          *runner.Usage `json:"last_usage"`
}

// TodayView is the totals of the kind's runs since the start of the day.
type TodayView struct {
	Runs         int     `json:"runs"`
	Blocked      int     `json:"blocked"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	// UsageRuns is how many of Runs reported usage: the token and cost figures only
	// cover those.
	UsageRuns  int    `json:"usage_runs"`
	LastStatus string `json:"last_status,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	LastJobID  string `json:"last_job_id,omitempty"`
	LastAt     int64  `json:"last_at,omitempty"`
}

// PolicyView is the rotation policy; a zero field means that rule is off.
type PolicyView struct {
	MaxRuns         int     `json:"max_runs"`
	MaxContextRatio float64 `json:"max_context_ratio"`
	MaxAgeSec       int64   `json:"max_age_sec"`
}

// ResetView is the answer of a manual reset: how many sessions it ended and the kind as it
// reads afterwards.
type ResetView struct {
	Version int      `json:"version"`
	Reset   int64    `json:"reset"`
	Kind    KindView `json:"kind"`
}

// NewKindView converts a Status result into its wire form.
func NewKindView(st KindStatus) KindView {
	v := KindView{
		Kind:     st.Kind,
		Sessions: make([]SessionView, 0, len(st.Sessions)),
		Today: TodayView{
			Runs: st.Today.Runs, Blocked: st.Today.Blocked,
			InputTokens: st.Today.InputTokens, OutputTokens: st.Today.OutputTokens, TotalTokens: st.Today.TotalTokens,
			CostUSD: st.Today.CostUSD, UsageRuns: st.Today.UsageRuns,
			LastStatus: st.Today.LastStatus, LastError: st.Today.LastError, LastJobID: st.Today.LastJobID, LastAt: st.Today.LastAt,
		},
		Policy: PolicyView{MaxRuns: st.Policy.MaxRuns, MaxContextRatio: st.Policy.MaxContextRatio,
			MaxAgeSec: int64(st.Policy.MaxAge / time.Second)},
	}
	for _, s := range st.Sessions {
		sv := SessionView{ID: s.ID, LastJobID: s.LastJobID, Agent: s.Agent, Project: s.Project, WorkDir: s.WorkDir,
			Model: s.Model, Runs: s.Runs, CreatedAt: s.CreatedAt, LastUsedAt: s.LastUsedAt,
			ContextRatio: s.ContextRatio, RotationReason: s.RotationReason, LastUsage: s.LastUsage}
		if s.ContextUsed > 0 {
			used := s.ContextUsed
			sv.ContextUsedTokens = &used
		}
		if s.ContextLimit > 0 {
			limit := s.ContextLimit
			sv.ContextLimitTokens = &limit
		}
		v.Sessions = append(v.Sessions, sv)
	}
	return v
}

// NewView assembles the versioned view of the given kinds.
func NewView(kinds ...KindStatus) View {
	v := View{Version: ViewVersion, Kinds: make([]KindView, 0, len(kinds))}
	for _, st := range kinds {
		v.Kinds = append(v.Kinds, NewKindView(st))
	}
	return v
}

// ResetKind marks every active session of kind (all projects and work dirs) as manually
// reset, so the next run of each is fresh; see Reset. n is how many sessions it ended.
func (r *Runner) ResetKind(kind, actor string) (int64, error) {
	list, err := r.store.ListBackgroundSessions(kind)
	if err != nil {
		return 0, err
	}
	type scope struct{ project, workDir string }
	seen := map[scope]bool{}
	var total int64
	for _, b := range list {
		sc := scope{b.Project, b.WorkDir}
		if seen[sc] {
			continue
		}
		seen[sc] = true
		n, err := r.Reset(kind, b.Project, b.WorkDir, actor)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
