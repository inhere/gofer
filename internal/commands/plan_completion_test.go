package commands

import (
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestPlanCompletionFormat(t *testing.T) {
	pct := func(v int) *int { return &v }
	cases := []struct {
		name  string
		plan  client.Plan
		long  string
		short string
	}{
		{"older server, no manual progress", client.Plan{}, "", "—"},
		{"older server, manual progress", client.Plan{Progress: 40}, "40", "—"},
		{"nothing to measure",
			client.Plan{Completion: &jobstore.PlanCompletion{Basis: jobstore.CompletionNone}},
			"—", "—"},
		{"todos with jobs",
			client.Plan{
				Completion: &jobstore.PlanCompletion{Basis: jobstore.CompletionTodos, Done: 5, Total: 6, Percent: pct(83)},
				Counts:     &jobstore.PlanCounts{Total: 11, Done: 10, Failed: 1},
			},
			"5/6 todos (83%) · jobs: 10 done / 1 failed / 11 total", "5/6 todos"},
		{"todos only",
			client.Plan{
				Completion: &jobstore.PlanCompletion{Basis: jobstore.CompletionTodos, Done: 2, Total: 3, Percent: pct(67)},
				Counts:     &jobstore.PlanCounts{},
			},
			"2/3 todos (67%)", "2/3 todos"},
		{"jobs basis with a failure",
			client.Plan{
				Completion: &jobstore.PlanCompletion{Basis: jobstore.CompletionJobs, Done: 10, Total: 11, Percent: pct(91)},
				Counts:     &jobstore.PlanCounts{Total: 11, Done: 10, Failed: 1},
			},
			"10/11 jobs (91%) · 1 failed", "10/11 jobs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatCompletion(tc.plan); got != tc.long {
				t.Fatalf("formatCompletion = %q, want %q", got, tc.long)
			}
			if got := formatCompletionShort(tc.plan); got != tc.short {
				t.Fatalf("formatCompletionShort = %q, want %q", got, tc.short)
			}
		})
	}
}
