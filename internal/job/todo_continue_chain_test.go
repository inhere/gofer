package job

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// seedHandovers stores a done session chain of n hand-overs ending in the predecessor
// (x-0 <- x-1 <- ... <- pred): n TodoID changes along resumed_from.
func seedHandovers(t *testing.T, s *Service, n int) {
	t.Helper()
	prev := ""
	for i := 0; i < n; i++ {
		id := "j-" + string(rune('a'+i))
		p := prev
		seedContJob(t, s, id, "x-"+id, "sess-p", StatusDone, int64(10+i), func(j *JobResult) { j.ResumedFrom = p })
		prev = id
	}
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) { j.ResumedFrom = prev })
}

// setChainLimit reloads the service with the project's continue_max_chain set (nil = unset).
func setChainLimit(s *Service, limit *int) {
	cfg := *s.config()
	projects := map[string]config.ProjectConfig{}
	for k, p := range cfg.Projects {
		p.ContinueMaxChain = limit
		projects[k] = p
	}
	cfg.Projects = projects
	s.Reload(&cfg)
}

// TestTodoContinueChainLimitConfig: condition 8 reads the project's continue_max_chain:
// the default is 3, 0 means unlimited, explicit values move the limit.
func TestTodoContinueChainLimitConfig(t *testing.T) {
	ptr := func(n int) *int { return &n }
	cases := []struct {
		name  string
		limit *int
		chain int
		want  string // "" = continues
	}{
		{"default, below", nil, 2, ""},
		{"default, at the limit", nil, 3, "已连续接续 3 次"},
		{"zero is unlimited", ptr(0), 5, ""},
		{"explicit 1, at the limit", ptr(1), 1, "已连续接续 1 次"},
		{"explicit 1, below", ptr(1), 0, ""},
		{"explicit 5, below", ptr(5), 4, ""},
		{"explicit 5, at the limit", ptr(5), 5, "已连续接续 5 次"},
		{"ceiling 10, at the limit", ptr(10), 10, "已连续接续 10 次"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newContinueService(t)
			seedContinuePlan(t, s, nil)
			setChainLimit(s, tc.limit)
			seedHandovers(t, s, tc.chain)
			got := continueDecision(t, s, contSucc)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("reason = %q, want a continuation", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tc.want)
			}
			if by := continuedBy(t, s, contPred); by != "" {
				t.Errorf("a fallback must not hold the slot, continued_by = %q", by)
			}
		})
	}
}

// TestTodoContinueChainNotCountedForSameTodo: automatic continuations and rework keep
// the TodoID along resumed_from and do not add to the chain length.
func TestTodoContinueChainNotCountedForSameTodo(t *testing.T) {
	s, _ := newContinueService(t)
	seedContinuePlan(t, s, nil)
	seedContJob(t, s, "j-a", "x-a", "sess-p", StatusDone, 10, nil)
	seedContJob(t, s, "j-a2", "x-a", "sess-p", StatusDone, 11, func(j *JobResult) { j.ResumedFrom = "j-a" })  // auto-resume
	seedContJob(t, s, "j-a3", "x-a", "sess-p", StatusDone, 12, func(j *JobResult) { j.ResumedFrom = "j-a2" }) // rework
	seedContJob(t, s, "j-pred", contPred, "sess-p", StatusDone, 100, func(j *JobResult) { j.ResumedFrom = "j-a3" })
	pred, _ := s.Get("j-pred")
	if n := s.continuationChainLength(pred); n != 1 {
		t.Fatalf("chain length = %d, want 1 (only the hand-over to the predecessor's todo counts)", n)
	}
	if got := continueDecision(t, s, contSucc); got != "" {
		t.Fatalf("reason = %q, want a continuation", got)
	}
}
