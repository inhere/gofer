package commands

import (
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
