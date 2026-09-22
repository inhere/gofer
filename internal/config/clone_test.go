package config

import "testing"

// TestCloneIndependentsServerBlock pins the widening that WEB-04③ V1.1 forced: the
// console edits `server` through Core.Update, whose clone→mutate→publish transaction
// is only safe if a mutation on the clone cannot be seen by a reader holding the
// previous generation. Before this, Server was shared wholesale (D-MED-7), so
// `next.Server.Retry.MaxAttempts = 3` would have edited the LIVE config that in-flight
// Submit calls are reading.
func TestCloneIndependentsServerBlock(t *testing.T) {
	yes, no := true, false
	autoResume, stall, recoverWindow := 1, 900, 120
	orig := &Config{Server: ServerConfig{
		Callers:             []CallerConfig{{ID: "a", CanAdmin: true}},
		WebEnabled:          &yes,
		Workers:             map[string]WorkerAuthConfig{"w": {Labels: []string{"x"}}},
		Notification:        &NotificationConfig{Webhooks: []WebhookConfig{{URL: "u", Events: []string{"job.terminal"}}}, AllowHosts: []string{"h"}},
		Metrics:             MetricsConfig{Enabled: &no},
		Retry:               &RetryPolicy{MaxAttempts: 2, BackoffSec: []int{30}, OnExitCodes: []int{7}},
		AgentFallback:       &AgentFallbackConfig{OnFailure: &yes},
		AgentHealth:         &AgentHealthConfig{WindowSec: 60},
		AutoResumeMax:       &autoResume,
		StallTimeoutSec:     &stall,
		JobRecoverWindowSec: &recoverWindow,
		DirLock:             &yes,
		MaxJobTimeoutSec:    3600,
	}}

	clone := orig.Clone()

	// Mutate THROUGH every pointer/slice/map the clone exposes.
	clone.Server.Callers[0].CanAdmin = false
	*clone.Server.WebEnabled = false
	clone.Server.Workers["w"] = WorkerAuthConfig{Labels: []string{"y"}}
	clone.Server.Notification.Webhooks[0].Events[0] = "job.stalled"
	clone.Server.Notification.AllowHosts[0] = "h2"
	*clone.Server.Metrics.Enabled = true
	clone.Server.Retry.MaxAttempts = 9
	clone.Server.Retry.BackoffSec[0] = 99
	clone.Server.Retry.OnExitCodes[0] = 0
	*clone.Server.AgentFallback.OnFailure = false
	clone.Server.AgentHealth.WindowSec = 0
	*clone.Server.AutoResumeMax = 5
	*clone.Server.StallTimeoutSec = 5
	*clone.Server.JobRecoverWindowSec = 5
	*clone.Server.DirLock = false
	clone.Server.MaxJobTimeoutSec = 1

	if !orig.Server.Callers[0].CanAdmin {
		t.Error("Callers slice shared with the source")
	}
	if !*orig.Server.WebEnabled {
		t.Error("WebEnabled pointer shared with the source")
	}
	if orig.Server.Workers["w"].Labels[0] != "x" {
		t.Error("Workers map shared with the source")
	}
	if orig.Server.Notification.Webhooks[0].Events[0] != "job.terminal" || orig.Server.Notification.AllowHosts[0] != "h" {
		t.Error("Notification block shared with the source")
	}
	if *orig.Server.Metrics.Enabled {
		t.Error("Metrics.Enabled pointer shared with the source")
	}
	if orig.Server.Retry.MaxAttempts != 2 || orig.Server.Retry.BackoffSec[0] != 30 || orig.Server.Retry.OnExitCodes[0] != 7 {
		t.Error("Retry policy shared with the source")
	}
	if !*orig.Server.AgentFallback.OnFailure {
		t.Error("AgentFallback.OnFailure pointer shared with the source")
	}
	if orig.Server.AgentHealth.WindowSec != 60 {
		t.Error("AgentHealth block shared with the source")
	}
	for name, got := range map[string]int{
		"auto_resume_max":        *orig.Server.AutoResumeMax,
		"stall_timeout_sec":      *orig.Server.StallTimeoutSec,
		"job_recover_window_sec": *orig.Server.JobRecoverWindowSec,
	} {
		if got == 5 {
			t.Errorf("server.%s pointer shared with the source", name)
		}
	}
	if !*orig.Server.DirLock || orig.Server.MaxJobTimeoutSec != 3600 {
		t.Error("scalar server fields leaked through the clone")
	}
}
