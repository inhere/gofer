package commands

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
)

// TestPrintJobDiagnosisShowsReasonAndNextSteps: the diagnose screen says WHY the job
// ended (enum plus its label), keeps every timeline stage visible — marking the ones
// gofer has no record of as unknown instead of printing a zero time — names the earliest
// failure of the chain, and ends with the next steps.
func TestPrintJobDiagnosisShowsReasonAndNextSteps(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.EN))
	c := bindCmd(NewJobCmd().Subs[0])
	d := job.Diagnosis{
		JobID:       "job-7",
		Status:      job.StatusFailed,
		Finished:    true,
		Reason:      job.TerminationAgentError,
		ReasonLabel: "agent failure",
		Error:       "boom",
		Timeline: []job.DiagnosisStage{
			{Stage: job.DiagnosisStageQueued, Label: "Queued", At: 1700000000, Present: true},
			{Stage: job.DiagnosisStageFirstOutput, Label: "First output", Note: "gofer does not record it"},
		},
		Chain: []job.DiagnosisLink{
			{JobID: "job-6", Relation: "resume", Status: job.StatusFailed, Reason: job.TerminationTransient, Error: "provider 500"},
			{JobID: "job-7", Relation: "target", Status: job.StatusFailed, Reason: job.TerminationAgentError, Error: "boom"},
		},
		FirstFailure: &job.DiagnosisLink{JobID: "job-6", Status: job.StatusFailed, Reason: job.TerminationTransient, Error: "provider 500"},
		Evidence:     []string{"status=failed", "exit_code=3"},
		Unknowns:     []string{"First output: gofer does not record it"},
		NextSteps:    []string{"The work itself failed: fix the cause and submit again."},
	}
	out := captureOutput(t, func() { printJobDiagnosis(c, d) })

	for _, want := range []string{
		"reason:      agent_error (agent failure)",
		"unknown (gofer does not record it)",
		"first failure: job-6 (transient) provider 500",
		"- status=failed",
		"next steps:",
		"- The work itself failed: fix the cause and submit again.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestPrintJobDiagnosisIsBilingual: the screen's own headings come from i18n, so a zh-CN
// CLI explains the job in Chinese while the diagnosis content stays whatever the server
// derived (its own language is the request's).
func TestPrintJobDiagnosisIsBilingual(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.ZhCN))
	c := bindCmd(NewJobCmd().Subs[0])
	d := job.Diagnosis{
		JobID:    "job-7",
		Status:   job.StatusFailed,
		Finished: true,
		Reason:   job.TerminationAgentError,
		Timeline: []job.DiagnosisStage{
			{Stage: job.DiagnosisStageFirstOutput, Label: "首个输出", Note: "gofer 不记录它"},
		},
		Chain:     []job.DiagnosisLink{{JobID: "job-7", Relation: "target", Status: job.StatusFailed, Reason: job.TerminationAgentError}},
		Evidence:  []string{"status=failed"},
		Unknowns:  []string{"首个输出: gofer 不记录它"},
		NextSteps: []string{"先看错误再重提。"},
	}
	out := captureOutput(t, func() { printJobDiagnosis(c, d) })

	for _, want := range []string{
		"时间线:",
		"未知（gofer 不记录它）",
		"续跑链:",
		"证据:",
		"未知:",
		"下一步建议:",
		"- 先看错误再重提。",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("zh output missing %q:\n%s", want, out)
		}
	}
}

// TestJobDiagnoseCommand: `job diagnose <id>` asks the server for that job's diagnosis at
// GET /v1/jobs/{id}/diagnosis and prints the derived reason plus the next steps; --json
// prints the raw structure instead of the screen.
func TestJobDiagnoseCommand(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.EN))
	var gotMethod, gotPath string
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.Diagnosis{
			JobID: "job-7", Status: job.StatusFailed, Finished: true,
			Reason: job.TerminationStalled, ReasonLabel: "Stalled: no output",
			Timeline: []job.DiagnosisStage{{Stage: job.DiagnosisStageStarted, Label: "Started", At: 1700000000, Present: true}},
			Chain:    []job.DiagnosisLink{{JobID: "job-7", Relation: "target", Status: job.StatusFailed, Reason: job.TerminationStalled}},
			Evidence: []string{"status=failed"},
			NextSteps: []string{
				"The job produced no output inside its stall window: retry it.",
			},
		})
	})

	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"job", "diagnose", "--server", ts.URL, "job-7"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if gotMethod != http.MethodGet || gotPath != "/v1/jobs/job-7/diagnosis" {
		t.Fatalf("request = %s %s, want GET /v1/jobs/job-7/diagnosis", gotMethod, gotPath)
	}
	for _, want := range []string{"reason:      stalled (Stalled: no output)", "next steps:", "- The job produced no output"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	jsonOut := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"job", "diagnose", "--server", ts.URL, "job-7", "--json"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(jsonOut, "\"termination_reason\": \"stalled\"") || strings.Contains(jsonOut, "next steps:") {
		t.Fatalf("--json did not print the raw structure:\n%s", jsonOut)
	}
}
