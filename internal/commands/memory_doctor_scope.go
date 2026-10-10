package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// staleCandidateAge is how long a pending knowledge candidate may wait before
// `memory doctor` reminds about it (gofer-3nxa.8).
const staleCandidateAge = 30 * 24 * time.Hour

// StaleCandidate is one pending knowledge candidate older than staleCandidateAge.
type StaleCandidate struct {
	ID         int64  `json:"id"`
	ProjectKey string `json:"project_key"`
	JobID      string `json:"job_id"`
	AgeDays    int    `json:"age_days"`
	Text       string `json:"text"`
}

// memoryDoctorOutput is the `memory doctor --json` shape: the doctor report plus the
// stale candidates (omitted when there are none).
type memoryDoctorOutput struct {
	tracker.DoctorReport
	StaleCandidates []StaleCandidate `json:"stale_candidates,omitempty"`
}

// scopedDoctorReport runs the repository doctor on server-scoped memories, client side.
// Only the checks that need the memory itself apply (flagged, handoff-expired,
// note-stale, summary-missing, duplicate; a memory's own doctor_ignore still applies):
// path-missing and commit-missing need a checkout and are skipped, and there is no
// repository prime.doctor.suppress.
func scopedDoctorReport(items []client.ScopedMemory, now time.Time) tracker.DoctorReport {
	mems := make([]tracker.Memory, 0, len(items))
	for _, it := range items {
		if it.Deleted {
			continue
		}
		mems = append(mems, it.TrackerMemory())
	}
	return tracker.DiagnoseMemories(mems, tracker.DoctorOptions{Now: now})
}

// staleCandidates keeps the pending candidates created more than staleCandidateAge ago.
func staleCandidates(cands []jobstore.MemoryCandidate, now time.Time) []StaleCandidate {
	var out []StaleCandidate
	for _, m := range cands {
		if m.Status != "" && m.Status != "pending" {
			continue
		}
		age := now.Sub(time.Unix(m.CreatedAt, 0))
		if age <= staleCandidateAge {
			continue
		}
		out = append(out, StaleCandidate{ID: m.ID, ProjectKey: m.ProjectKey, JobID: m.JobID, AgeDays: int(age / (24 * time.Hour)), Text: m.Text})
	}
	return out
}

// formatStaleCandidates is the human block appended to the doctor report.
func formatStaleCandidates(items []StaleCandidate) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "stale knowledge candidates (pending > %d days; `gofer memory accept <id> --key <k>` or `memory reject <id>`):\n", int(staleCandidateAge/(24*time.Hour)))
	for _, it := range items {
		text := []rune(strings.Join(strings.Fields(it.Text), " "))
		if len(text) > 80 {
			text = append(text[:80], '…')
		}
		fmt.Fprintf(&b, "  - #%d %s %s (%d days) %s\n", it.ID, it.ProjectKey, it.JobID, it.AgeDays, string(text))
	}
	return b.String()
}

// pendingStaleCandidates asks the server for a project's pending candidates and keeps
// the stale ones.
func pendingStaleCandidates(cli *client.Client, project string, now time.Time) ([]StaleCandidate, error) {
	rows, err := cli.ListMemoryCandidates(client.MemoryCandidateListOpts{Project: project})
	if err != nil {
		return nil, err
	}
	return staleCandidates(rows, now), nil
}
