package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestStewardMemoryFindingsAndSuggest(t *testing.T) {
	call, meta, _ := newStewardSession(t)
	old := time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	assert.NoErr(t, meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "trk-1", ProjectKey: "self"}))
	body, _ := json.Marshal(map[string]any{"key": "old-note", "content": "an old observation", "updated_at": old, "kind": "note"})
	assert.NoErr(t, meta.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: "trk-1", ID: "old-note", Body: body, Rev: 1, UpdatedAt: old}))

	isErr, out, text := call("gofer_memory_findings", map[string]any{})
	assert.False(t, isErr, text)
	findings, _ := out["findings"].([]any)
	assert.Len(t, findings, 1)
	assert.Require(t, len(findings) == 1)
	f := findings[0].(map[string]any)
	assert.Eq(t, "old-note", f["key"])
	assert.Eq(t, []any{"archive"}, f["actions"])

	isErr, out, text = call("gofer_memory_suggest", map[string]any{"tracker_id": "trk-1", "key": "old-note", "action": "archive", "reason": "200 天未更新"})
	assert.False(t, isErr, text)
	assert.Eq(t, true, out["recorded"])

	// Proposed: no longer an open finding, and a 「今天」 memory card.
	_, out, _ = call("gofer_memory_findings", map[string]any{})
	findings, _ = out["findings"].([]any)
	assert.Len(t, findings, 0)
	isErr, out, text = call("gofer_today_list", map[string]any{"kind": "memory"})
	assert.False(t, isErr, text)
	cards, _ := out["cards"].([]any)
	assert.Len(t, cards, 1)
	assert.True(t, strings.HasPrefix(cards[0].(map[string]any)["key"].(string), "memory:"))

	// Invalid action is refused.
	isErr, _, _ = call("gofer_memory_suggest", map[string]any{"tracker_id": "trk-1", "key": "old-note", "action": "delete", "reason": "x"})
	assert.True(t, isErr)
}
