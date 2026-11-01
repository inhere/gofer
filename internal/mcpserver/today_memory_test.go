package mcpserver

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestStewardMemoryFindings(t *testing.T) {
	call, meta, _ := newStewardSession(t)
	old := time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	assert.NoErr(t, meta.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "trk-1", ProjectKey: "self"}))
	body, _ := json.Marshal(map[string]any{"key": "old-note", "content": "an old observation", "updated_at": old, "kind": "note"})
	assert.NoErr(t, meta.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: "trk-1", ID: "old-note", Body: body, Rev: 1, UpdatedAt: old}))

	isErr, out, text := call("gofer_memory_findings", map[string]any{})
	assert.False(t, isErr, text)
	findings, _ := out["findings"].([]any)
	assert.Require(t, len(findings) == 1)
	f := findings[0].(map[string]any)
	assert.Eq(t, "old-note", f["key"])
	_, hasActions := f["actions"]
	assert.False(t, hasActions)
}

// The model-written memory suggestion queue is gone: the tool is not registered.
func TestMemorySuggestToolIsGone(t *testing.T) {
	t.Setenv(envSteward, "1")
	t.Setenv(envJobID, "steward-job")
	session := connectTo(t, newServer(NewClientBackend(client.New("http://127.0.0.1:1", "tok")), ""))
	res, err := session.ListTools(t.Context(), nil)
	assert.NoErr(t, err)
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	assert.False(t, names["gofer_memory_suggest"])
	assert.True(t, names["gofer_memory_findings"])
}
