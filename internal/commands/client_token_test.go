package commands

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestCLIUsesJobTokenInsideJob is the SEC-01 CLI half: inside a job the client's
// default bearer token is the job's OWN credential (GOFER_JOB_TOKEN), because the
// server token is no longer in the environment at all — a `gofer job comment` typed by
// an agent must authenticate as that job, not fall back to "no token". An explicit
// --token still wins, and outside a job the previous chain is untouched.
func TestCLIUsesJobTokenInsideJob(t *testing.T) {
	sc := &config.ServerConfig{Token: "tok-config"}

	t.Setenv(job.EnvJobToken, "gjt_job_1_abcdef")
	t.Setenv("GOFER_SERVER_TOKEN", "tok-server")
	if got := resolveClientToken(sc, ""); got != "gjt_job_1_abcdef" {
		t.Fatalf("token inside a job = %q, want the job credential", got)
	}
	// An explicit --token still overrides: a human (or a script that must act as a user)
	// can deliberately step out of the job's identity.
	if got := resolveClientToken(sc, "tok-flag"); got != "tok-flag" {
		t.Fatalf("explicit --token = %q, want tok-flag", got)
	}

	// Outside a job nothing changed: env, then config.
	t.Setenv(job.EnvJobToken, "")
	if got := resolveClientToken(sc, ""); got != "tok-server" {
		t.Fatalf("token outside a job = %q, want GOFER_SERVER_TOKEN", got)
	}
	t.Setenv("GOFER_SERVER_TOKEN", "")
	if got := resolveClientToken(sc, ""); got != "tok-config" {
		t.Fatalf("token fallback = %q, want the config token", got)
	}
}
