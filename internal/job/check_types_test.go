package job

import (
	"strings"
	"testing"
)

func TestCheckResultParserContract(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"}`,
		`{"schema":"job-check-result/v1","verdict":"fail","items":[{"id":"a","requirement":"r","passed":false,"evidence":"missing"}],"summary":"no"}`,
		`{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok","extra":1}`,
		// One fence around the whole output is tolerated (models add it anyway).
		"```json\n" + `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"}` + "\n```",
		"  ```\n" + `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"}` + "\n```\n",
	} {
		if _, err := ParseCheckResult([]byte(raw)); err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
	}
	for name, raw := range map[string]string{
		"fail without items": `{"schema":"job-check-result/v1","verdict":"fail","items":[],"summary":"bad"}`,
		"trailing text":      `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"} trailing`,
		"two objects":        `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"a"}{"x":1}`,
		"wrong schema":       `{"schema":"v2","verdict":"pass","items":[],"summary":"ok"}`,
		"unknown verdict":    `{"schema":"job-check-result/v1","verdict":"maybe","items":[],"summary":"ok"}`,
		"item without id":    `{"schema":"job-check-result/v1","verdict":"fail","items":[{"id":" ","requirement":"r"}],"summary":"x"}`,
		"not json":           `pass`,
		"prose around fence": "Here it is:\n```json\n" + `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"}` + "\n```",
		"non-json fence":     "```yaml\n" + `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"}` + "\n```",
		"empty":              ``,
		"oversized":          `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"` + strings.Repeat("x", CheckResultMaxBytes) + `"}`,
		"summary too long":   `{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"` + strings.Repeat("x", CheckTextMaxBytes+1) + `"}`,
	} {
		if _, err := ParseCheckResult([]byte(raw)); err == nil {
			t.Fatalf("%s: accepted invalid result", name)
		}
	}
}

func TestCheckInputParserContract(t *testing.T) {
	ok := `{"schema":"job-check/v1","requirements":"r","report_tail":"t","commits":[],"diff_summary":{"files":1,"insertions":2,"deletions":0}}`
	if in, err := ParseCheckInput([]byte(ok)); err != nil || in.DiffSummary.Files != 1 {
		t.Fatalf("parse input: %+v %v", in, err)
	}
	for name, raw := range map[string]string{
		"wrong schema":    `{"schema":"x","requirements":"r"}`,
		"no requirements": `{"schema":"job-check/v1","requirements":"  "}`,
		"trailing":        ok + `x`,
		"empty":           ``,
		"tail too long":   `{"schema":"job-check/v1","requirements":"r","report_tail":"` + strings.Repeat("x", CheckTextMaxBytes+1) + `"}`,
	} {
		if _, err := ParseCheckInput([]byte(raw)); err == nil {
			t.Fatalf("%s: accepted invalid input", name)
		}
	}
}

func TestCheckPromptLimit(t *testing.T) {
	if got, err := NormalizeCheckPrompt("  ok \n"); err != nil || got != "ok" {
		t.Fatalf("normalize = %q %v", got, err)
	}
	if _, err := NormalizeCheckPrompt(strings.Repeat("x", CheckPromptMaxBytes)); err != nil {
		t.Fatalf("rejected a prompt at the limit: %v", err)
	}
	if _, err := NormalizeCheckPrompt(strings.Repeat("x", CheckPromptMaxBytes+1)); err == nil {
		t.Fatal("accepted oversized prompt")
	}
}

func TestNormalizeCheckPromptSource(t *testing.T) {
	cases := []struct {
		source, prompt, want string
		bad                  bool
	}{
		{"", "req", "explicit", false},
		{"", "", "", false},
		{"explicit", "req", "explicit", false},
		{" prompt_block ", "req", "prompt_block", false},
		{"magic", "req", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeCheckPromptSource(c.source, c.prompt)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("NormalizeCheckPromptSource(%q,%q) = %q, %v", c.source, c.prompt, got, err)
		}
	}
}

func TestDeliveryCheckEventDetailJSON(t *testing.T) {
	if EventDeliveryCheckFailed != "job.delivery_check.failed" || EventDeliveryCheckAutoResumed != "job.delivery_check.auto_resumed" {
		t.Fatal("event names drifted")
	}
}
