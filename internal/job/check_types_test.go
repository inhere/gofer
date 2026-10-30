package job

import "testing"

func TestCheckResultParserContract(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"}`,
		`{"schema":"job-check-result/v1","verdict":"fail","items":[{"id":"a","requirement":"r","passed":false,"evidence":"missing"}],"summary":"no"}`,
	} {
		if _, err := ParseCheckResult([]byte(raw)); err != nil { t.Fatalf("parse %s: %v", raw, err) }
	}
	for _, raw := range []string{
		`{"schema":"job-check-result/v1","verdict":"fail","items":[],"summary":"bad"}`,
		`{"schema":"job-check-result/v1","verdict":"pass","items":[],"summary":"ok"} trailing`,
	} {
		if _, err := ParseCheckResult([]byte(raw)); err == nil { t.Fatalf("accepted invalid result %s", raw) }
	}
}

func TestCheckPromptLimit(t *testing.T) {
	if _, err := NormalizeCheckPrompt("  ok \n"); err != nil { t.Fatal(err) }
	if _, err := NormalizeCheckPrompt(string(make([]byte, CheckPromptMaxBytes+1))); err == nil { t.Fatal("accepted oversized prompt") }
}
