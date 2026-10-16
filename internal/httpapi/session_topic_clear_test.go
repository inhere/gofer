package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// An explicit "topic": "" clears the stored topic (the key is present); an omitted topic is kept.
func TestPatchSessionTopicEmptyStringClears(t *testing.T) {
	s := topicServer(t)
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-c", CallerID: "alice"})

	resp := patchSession(t, s, "tok-alice", "sess-c", "", map[string]any{"topic": "写收集箱", "scope": []string{"web/src"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// Only the scope changes: the topic is kept.
	resp = patchSession(t, s, "tok-alice", "sess-c", "", map[string]any{"scope": []string{"web"}})
	var v sessionView
	decode(t, resp, &v)
	if v.Topic != "写收集箱" {
		t.Fatalf("an omitted topic was not kept: %+v", v)
	}

	resp = patchSession(t, s, "tok-alice", "sess-c", "", map[string]any{"topic": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var cleared sessionView // omitempty fields stay in a reused struct
	decode(t, resp, &cleared)
	v = cleared
	if v.TopicSource == "human" || v.Topic == "写收集箱" {
		t.Fatalf("topic not cleared: %+v", v)
	}
	if strings.Join(v.Scope, ",") != "web" {
		t.Fatalf("clearing the topic touched the scope: %+v", v.Scope)
	}
	got, _, _ := s.jobs.Meta().GetAgentSession("sess-c")
	if got.Topic != "" {
		t.Fatalf("stored topic = %q, want empty", got.Topic)
	}
}
