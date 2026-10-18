package httpapi

import (
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"net/http"
	"strings"
	"testing"
)

// Memory and tracker writes under a ceiling (plan S6): every write route is exercised
// explicitly, for a user caller and for the credential of a restricted job.

func isGateOutOfScope(status int, body string) bool {
	return status == http.StatusNotFound && strings.Contains(body, `"error":"not found"`)
}

type ceilingWrite struct {
	name   string
	method string
	path   string
	body   any
}

// TestCeilingMemoryWrites: project-scope memory writes only for in-scope projects, any other
// scope only with global_memory.
func TestCeilingMemoryWrites(t *testing.T) {
	s := newCeilingServer(t)
	seedCeilingWorld(t, s, "p1")
	seedCeilingWorld(t, s, "p2")
	grantAll(s)

	mem := func(scope, key string) []ceilingWrite {
		bodyKey := key
		if scope == "global" {
			bodyKey = "" // a global memory has no scope key
		}
		return []ceilingWrite{
			{"POST /v1/memories", "POST", "/v1/memories", map[string]any{"scope": scope, "scope_key": bodyKey, "key": "k1", "content": "c"}},
			{"PUT", "PUT", "/v1/memories/" + scope + "/" + key + "/k2", map[string]any{"content": "c"}},
			{"DELETE", "DELETE", "/v1/memories/" + scope + "/" + key + "/mem-key", nil},
			{"flag", "POST", "/v1/memories/" + scope + "/" + key + "/mem-key/flag", map[string]any{"kind": "stale"}},
			{"unflag", "DELETE", "/v1/memories/" + scope + "/" + key + "/mem-key/flag", nil},
		}
	}
	type scenario struct {
		scope, key string
		token      string
		inside     bool
	}
	for _, sc := range []scenario{
		{"project", "p1", ceilTokLim, true},
		{"project", "p2", ceilTokLim, false},
		{"global", "x", ceilTokLim, false},  // global_memory not granted
		{"global", "x", ceilTokGM, true},    // global_memory granted
		{"project", "p2", ceilTokGM, false}, // global_memory does not widen projects
		{"project", "p2", ceilTokPlain, true},
	} {
		for _, w := range mem(sc.scope, sc.key) {
			status, body := ceilReq(t, s, sc.token, w.method, w.path, w.body)
			if sc.inside == isGateOutOfScope(status, body) {
				t.Errorf("%s %s %s as %s = %d (%s): inside=%v", w.name, w.method, w.path, sc.token, status, body, sc.inside)
			}
			if sc.inside && strings.Contains(body, "caller ceiling") {
				t.Errorf("%s %s as %s refused by the ceiling: %s", w.method, w.path, sc.token, body)
			}
		}
	}
	// The write that really lands: a restricted caller creates a project memory, not a foreign one.
	if status, body := ceilReq(t, s, ceilTokLim, "POST", "/v1/memories", map[string]any{"scope": "project", "scope_key": "p1", "key": "made", "content": "c"}); status != http.StatusOK {
		t.Errorf("in-scope memory create = %d (%s), want 200", status, body)
	}
	if status, body := ceilReq(t, s, ceilTokGM, "POST", "/v1/memories", map[string]any{"scope": "global", "key": "gmade", "content": "c"}); status != http.StatusOK {
		t.Errorf("global memory create with global_memory = %d (%s), want 200", status, body)
	}
}

// trackerWrites are the write routes of the tracker mirror, parameterized by tracker id.
func trackerWrites(tid, issue string) []ceilingWrite {
	q := "?tracker_id=" + tid
	return []ceilingWrite{
		{"sync", "POST", "/v1/tracker/sync", map[string]any{"tracker_id": tid}},
		{"repo sync", "POST", "/v1/tracker/repos/" + tid + "/sync", map[string]any{}},
		{"repo rename", "POST", "/v1/tracker/repos/" + tid + "/rename", map[string]any{"new_tracker_id": "x"}},
		{"issue create", "POST", "/v1/tracker/repos/" + tid + "/issues", map[string]any{"title": "t"}},
		{"issue batch", "POST", "/v1/tracker/issues/batch", map[string]any{"tracker_id": tid, "ids": []string{issue}, "set": map[string]any{}}},
		{"issue edit", "PUT", "/v1/tracker/issues/" + issue + q, map[string]any{"title": "t2"}},
		{"issue comment", "POST", "/v1/tracker/issues/" + issue + "/comments" + q, map[string]any{"text": "hi"}},
		{"memory edit", "PUT", "/v1/tracker/memories/m1" + q, map[string]any{"content": "c"}},
		{"memory delete", "DELETE", "/v1/tracker/memories/m1" + q, nil},
		{"repo delete", "DELETE", "/v1/tracker/repos/" + tid, nil},
	}
}

// TestCeilingTrackerWrites: tracker writes only for repos of in-scope projects, the global
// memory tracker only with global_memory.
func TestCeilingTrackerWrites(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)

	type scenario struct {
		tid, issue string
		token      string
		inside     bool
	}
	for _, sc := range []scenario{
		{out.trackerID, out.issueID, ceilTokLim, false},
		{"global", "x", ceilTokLim, false},
		{out.trackerID, out.issueID, ceilTokGM, false},
		{"global", "x", ceilTokGM, true},
		{in.trackerID, in.issueID, ceilTokLim, true},
	} {
		for _, w := range trackerWrites(sc.tid, sc.issue) {
			if w.name == "repo delete" && sc.inside && sc.tid != "global" {
				continue // destroys the fixture the next rows use; covered below
			}
			status, body := ceilReq(t, s, sc.token, w.method, w.path, w.body)
			if sc.inside == isGateOutOfScope(status, body) {
				t.Errorf("%s %s %s as %s = %d (%s): inside=%v", w.name, w.method, w.path, sc.token, status, body, sc.inside)
			}
			if strings.Contains(body, `"caller ceiling"`) {
				t.Errorf("%s %s as %s refused by the ceiling: %s", w.method, w.path, sc.token, body)
			}
		}
	}
	// A new repo may be registered under an in-scope project, not a foreign one.
	if status, body := ceilReq(t, s, ceilTokLim, "POST", "/v1/tracker/sync", map[string]any{"tracker_id": "trk-fresh", "project_key": "p2"}); !isGateOutOfScope(status, body) {
		t.Errorf("sync registering a repo under p2 = %d (%s), want 404", status, body)
	}
	if status, body := ceilReq(t, s, ceilTokLim, "POST", "/v1/tracker/sync", map[string]any{"tracker_id": "trk-fresh", "project_key": "p1"}); isGateOutOfScope(status, body) {
		t.Errorf("sync registering a repo under p1 = %d (%s), want it accepted", status, body)
	}
	// And the in-scope repo can be deleted by the restricted caller.
	if status, body := ceilReq(t, s, ceilTokLim, "DELETE", "/v1/tracker/repos/"+in.trackerID, nil); isGateOutOfScope(status, body) {
		t.Errorf("deleting an in-scope repo = %d (%s)", status, body)
	}
}

// TestCeilingJobCredentialMemoryAndTrackerWrites: the credential of a job whose owner is
// restricted gets the same ruling on the write routes SEC-01 opens, and nothing of what
// SEC-01 closes opens.
func TestCeilingJobCredentialMemoryAndTrackerWrites(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	out := seedCeilingWorld(t, s, "p2")
	grantAll(s)
	tok := seedCredentialJob(t, s, "cred-w", "p1", "lim")
	gmTok := seedCredentialJob(t, s, "cred-gm", "p1", "gm")

	// Memory flag is the one memory write a job credential has.
	flag := func(scope, key string) (int, string) {
		return ceilReq(t, s, tok, "POST", "/v1/memories/"+scope+"/"+key+"/mem-key/flag", map[string]any{"kind": "stale"})
	}
	if status, body := flag("project", "p2"); !isGateOutOfScope(status, body) {
		t.Errorf("flag of a p2 memory = %d (%s), want 404", status, body)
	}
	if status, body := flag("global", "x"); !isGateOutOfScope(status, body) {
		t.Errorf("flag of a global memory without global_memory = %d (%s), want 404", status, body)
	}
	if status, body := flag("project", "p1"); isGateOutOfScope(status, body) {
		t.Errorf("flag of a p1 memory = %d (%s), want it judged by the handler", status, body)
	}
	// The remaining memory writes stay closed to a job credential (SEC-01), in or out of scope.
	for _, w := range []ceilingWrite{
		{"create", "POST", "/v1/memories", map[string]any{"scope": "project", "scope_key": "p1", "key": "k", "content": "c"}},
		{"put", "PUT", "/v1/memories/project/p1/k", map[string]any{"content": "c"}},
		{"delete", "DELETE", "/v1/memories/project/p1/mem-key", nil},
	} {
		status, body := ceilReq(t, s, tok, w.method, w.path, w.body)
		if status != http.StatusForbidden || !strings.Contains(body, "job credential may not") {
			t.Errorf("job credential %s = %d (%s), want the SEC-01 refusal", w.name, status, body)
		}
	}
	// Tracker writes: sync is the only one a job credential has.
	sync := func(token, tid string) (int, string) {
		return ceilReq(t, s, token, "POST", "/v1/tracker/sync", map[string]any{"tracker_id": tid})
	}
	if status, body := sync(tok, out.trackerID); !isGateOutOfScope(status, body) {
		t.Errorf("sync of a p2 repo = %d (%s), want 404", status, body)
	}
	if status, body := sync(tok, "global"); !isGateOutOfScope(status, body) {
		t.Errorf("sync of the global tracker without global_memory = %d (%s), want 404", status, body)
	}
	if status, body := sync(gmTok, "global"); isGateOutOfScope(status, body) {
		t.Errorf("sync of the global tracker with global_memory = %d (%s), want it judged by the handler", status, body)
	}
	if status, body := sync(tok, in.trackerID); isGateOutOfScope(status, body) {
		t.Errorf("sync of a p1 repo = %d (%s), want it judged by the handler", status, body)
	}
	for _, w := range trackerWrites(out.trackerID, out.issueID) {
		if w.name == "sync" {
			continue
		}
		status, body := ceilReq(t, s, tok, w.method, w.path, w.body)
		if status/100 == 2 {
			t.Errorf("job credential %s %s = %d (%s), want a refusal", w.method, w.path, status, body)
		}
	}
}

// TestCeilingJobListPagesAfterTheProjectFilter: a ceiling naming one literal project is
// pushed into the job query, so a page is not emptied by newer jobs of other projects.
func TestCeilingJobListPagesAfterTheProjectFilter(t *testing.T) {
	s := newCeilingServer(t)
	in := seedCeilingWorld(t, s, "p1")
	seedCeilingWorld(t, s, "p2")
	grantAll(s)
	for i := 0; i < 3; i++ {
		id := "newer-p2-" + string(rune('a'+i))
		if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
			ID: id, ProjectKey: "p2", Agent: "exec", Runner: "worker", Status: job.StatusDone, Cwd: ".",
			StartedAt: time.Now().Unix() + 1000 + int64(i), UpdatedAt: time.Now().Unix() + 1000, CallerID: ceilOwner,
		}); err != nil {
			t.Fatal(err)
		}
	}
	status, body := ceilReq(t, s, ceilTokLim, "GET", "/v1/jobs?all=1&limit=1", nil)
	if status != http.StatusOK || !strings.Contains(body, in.jobID) || strings.Contains(body, "p2") {
		t.Errorf("GET /v1/jobs?limit=1 as the p1 caller = %d (%s), want the p1 job alone", status, body)
	}
}
