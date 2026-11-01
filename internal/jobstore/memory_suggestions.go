package jobstore

// The memory_suggestions table belonged to the steward's model-written memory cleanup
// queue (attention-surfaces review design §8), which is gone: no code reads or writes it
// any more. The table is kept (G057: a schema change must keep upgrading old databases,
// and rows older versions wrote stay harmless) and only cleared together with its tracker
// repository (DeleteTrackerRepo).

var memorySuggestionSchema = []string{
	`CREATE TABLE IF NOT EXISTS memory_suggestions (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  tracker_id   TEXT NOT NULL,
  memory_key   TEXT NOT NULL,
  action       TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  reason       TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL DEFAULT 'pending',
  by           TEXT NOT NULL DEFAULT '',
  job_id       TEXT NOT NULL DEFAULT '',
  base_rev     INTEGER NOT NULL DEFAULT 0,
  target_rev   INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  decided_at   INTEGER NOT NULL DEFAULT 0,
  decided_by   TEXT NOT NULL DEFAULT '',
  note         TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_suggestions_key ON memory_suggestions(tracker_id, memory_key, action, state)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_suggestions_state ON memory_suggestions(state, created_at)`,
}

func init() { schemaStmts = append(schemaStmts, memorySuggestionSchema...) }
