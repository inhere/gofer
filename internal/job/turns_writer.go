package job

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/store"
)

// openTurnsWriter opens the job's per-turn transcript sidecar (turns.jsonl) for an
// agent whose stream has a registered turn dialect (claude / suga). It returns
// (nil, nil, "") — nothing to write — for an agent without a dialect or a job without a
// result dir, and also when the file cannot be opened: the transcript is best-effort
// and never fails a job. Text is masked with the job's granted secret values before it
// is cut and encoded. The caller closes the returned file once the filter is closed.
func openTurnsWriter(resultDir, jobID, agentKey string, ac config.AgentConfig, secretValues []string) (*ndjsonfilter.TurnWriter, *os.File, string) {
	dialect := agent.NDJSONTurnsFor(agentKey, ac)
	if dialect == "" || resultDir == "" {
		return nil, nil, ""
	}
	f, err := os.OpenFile(filepath.Join(resultDir, store.TurnsFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("job.turns_sidecar", "job_id", jobID, "agent", agentKey, "err", err)
		return nil, nil, ""
	}
	return ndjsonfilter.NewTurnWriter(f, secretMaskFunc(secretValues)), f, dialect
}
