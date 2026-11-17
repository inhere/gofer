package commands

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// `gofer plan drive` (plan R4): the three entries hit PUT / DELETE / GET
// /v1/plans/{id}/driver with the flags the caller gave, and print the state the server
// answered. The level itself is the SERVER's to judge (one validator, one message), so the
// CLI forwards exactly what it was given — including `assist`, whose refusal is asserted
// through the server error.

// TestPlanDriveOnSendsLevelAndBudget: `plan drive on <plan> --level record --max-jobs 5
// --max-rework 2 --max-hours 1.5` PUTs that level and budget, and prints the stored state.
func TestPlanDriveOnSendsLevelAndBudget(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.PlanDriverStatus{
			PlanID: "plan-drive-cli-on", Enabled: true, Level: job.PlanDriverLevelRecord,
			Budget:    job.PlanDriverBudget{MaxJobs: 5, MaxRework: 2, MaxHours: 1.5},
			EnabledAt: 1700000000,
		})
	})

	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"plan", "drive", "on", "--server", ts.URL,
			"--level", "record", "--max-jobs", "5", "--max-rework", "2", "--max-hours", "1.5",
			"plan-drive-cli-on"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, out)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/plans/plan-drive-cli-on/driver" {
		t.Fatalf("request = %s %s, want PUT /v1/plans/plan-drive-cli-on/driver", gotMethod, gotPath)
	}
	if gotBody["level"] != job.PlanDriverLevelRecord {
		t.Fatalf("level sent = %v, want record", gotBody["level"])
	}
	budget, _ := gotBody["budget"].(map[string]any)
	if budget["max_jobs"] != float64(5) || budget["max_rework"] != float64(2) || budget["max_hours"] != 1.5 {
		t.Fatalf("budget sent = %v, want max_jobs 5 / max_rework 2 / max_hours 1.5", gotBody["budget"])
	}
	for _, want := range []string{"plan:    plan-drive-cli-on", "driver:  on (level=record)", "budget:  jobs 5 / rework 2 / hours 1.5", "since:   "} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestPlanDriveStatusAndOffHitTheirRoutes: status is a GET and off is a DELETE on the same
// path, and an off driver prints one line (its budget is not worth reading).
func TestPlanDriveStatusAndOffHitTheirRoutes(t *testing.T) {
	var calls []string
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.PlanDriverStatus{
			PlanID: "plan-drive-cli-off", Enabled: r.Method != http.MethodDelete,
			Level: job.PlanDriverLevelRecord,
		})
	})

	var out string
	var code int
	out = captureOutput(t, func() {
		code = NewApp("test").Run([]string{"plan", "drive", "status", "--server", ts.URL, "plan-drive-cli-off"})
	})
	if code != 0 || !strings.Contains(out, "driver:  on (level=record)") {
		t.Fatalf("status exit=%d out=%q", code, out)
	}
	if !strings.Contains(out, "budget:  jobs unlimited / rework unlimited / hours unlimited") {
		t.Fatalf("an unrestricted budget must name it unlimited:\n%s", out)
	}

	out = captureOutput(t, func() {
		code = NewApp("test").Run([]string{"plan", "drive", "off", "--server", ts.URL, "plan-drive-cli-off"})
	})
	if code != 0 || !strings.Contains(out, "driver:  off") {
		t.Fatalf("off exit=%d out=%q", code, out)
	}
	if len(calls) != 2 || calls[0] != "GET /v1/plans/plan-drive-cli-off/driver" ||
		calls[1] != "DELETE /v1/plans/plan-drive-cli-off/driver" {
		t.Fatalf("calls = %v, want GET then DELETE on /v1/plans/plan-drive-cli-off/driver", calls)
	}
}

// TestPlanDriveForwardsALaterLevel: a level this version does not support is not the CLI's
// to refuse — the server's 400 text reaches the caller unchanged, so the CLI and the HTTP
// API explain `assist` the same way.
func TestPlanDriveForwardsALaterLevel(t *testing.T) {
	var gotLevel string
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotLevel, _ = body["level"].(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":  "invalid plan driver",
			"detail": `autonomy level "assist" is supported in a later version; this version accepts record only`,
		})
	})

	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"plan", "drive", "on", "--server", ts.URL, "--level", "assist", "plan-drive-cli-later"})
	})
	if gotLevel != job.PlanDriverLevelAssist {
		t.Fatalf("level sent = %q, want assist forwarded", gotLevel)
	}
	if code == 0 {
		t.Fatalf("exit code = 0, want a failure for the refused level (out=%q)", out)
	}
}

// TestPlanDriveReportPrintsAgreement: `plan drive report <plan>` GETs the report route and
// prints the totals, the per-action agreement, the upgrade condition and the per-rule lines.
func TestPlanDriveReportPrintsAgreement(t *testing.T) {
	var gotMethod, gotPath string
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.PlanDriverReport{
			PlanID: "plan-drive-cli-rep", Enabled: true, Level: job.PlanDriverLevelRecord,
			Budget: job.PlanDriverBudget{MaxJobs: 9}, Spent: job.PlanDriverSpent{Jobs: 3, Rework: 1, Hours: 0.5},
			Total: 5, Pending: 1, WindowHours: 24,
			ByAction: []job.PlanDriverReportRow{{
				Action: "dispatch_todo", Total: 5, Match: 3, Mismatch: 1, Pending: 1, Rate: 0.75,
				RecentN: 4, RecentMatch: 3, RecentRate: 0.75,
			}},
			ByRule: []job.PlanDriverReportRow{{Action: "dispatch_todo", Rule: "dispatch_ready", Total: 5, Match: 3, Mismatch: 1, Pending: 1, Rate: 0.75}},
		})
	})
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"plan", "drive", "report", "--server", ts.URL, "plan-drive-cli-rep"})
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (%s)", code, out)
	}
	if gotMethod != http.MethodGet || gotPath != "/v1/plans/plan-drive-cli-rep/driver/report" {
		t.Fatalf("request = %s %s, want GET /v1/plans/plan-drive-cli-rep/driver/report", gotMethod, gotPath)
	}
	for _, want := range []string{
		"driver:  on (level=record)", "spent:   jobs 3 / rework 1 / hours 0.50", "5 recorded, 1 not observed yet",
		"dispatch_todo", "match 3  mismatch 1  no_followup 0  pending 1  agreement 75%",
		"upgrade condition (20 in a row at >= 90%): not yet", "dispatch_ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
