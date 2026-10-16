package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// 会话主题（第一期收集箱设计「会话主题与查找」）的 HTTP 面：PATCH、推断值、查找。
// 反向用例与调用断言在 authz_inhandler_test.go 的 "PATCH /v1/sessions/{sid}" 用例里；这里测语义。

const topicCtrRoot = "/ctr/self"

// topicServer is a deployment with no root administrator and every gate open: alice and bob
// hold nothing until a test installs database grants. The "self" project also has a
// container-side root, so a session running in a container is matched against it.
func topicServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServerCfgWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
		},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}, func(cfg *config.Config) {
		p := cfg.Projects["self"]
		p.ContainerPath = topicCtrRoot
		cfg.Projects["self"] = p
	})
	s.setDBGrants(nil)
	return s
}

func seedTopicSession(t *testing.T, s *Server, in jobstore.AgentSession) {
	t.Helper()
	now := time.Now().Unix()
	if in.Agent == "" {
		in.Agent = "claude"
	}
	if in.ProjectKey == "" {
		in.ProjectKey = "self"
	}
	if in.State == "" {
		in.State = jobstore.SessionIdle
	}
	if in.StartedAt == 0 {
		in.StartedAt = now
	}
	if in.LastSeenAt == 0 {
		in.LastSeenAt = now
	}
	if _, err := s.jobs.Meta().UpsertAgentSession(in); err != nil {
		t.Fatalf("seed session %s: %v", in.SessionID, err)
	}
}

func patchSession(t *testing.T, s *Server, token, sid, hdrSid string, body any) *http.Response {
	t.Helper()
	req := jsonRequest(t, http.MethodPatch, "/v1/sessions/"+sid, token, body)
	if hdrSid != "" {
		req.Header.Set(sessionHeader, hdrSid)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func jsonRequest(t *testing.T, method, path, token string, body any) *http.Request {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestPatchSessionTopicSourceAndPermissions(t *testing.T) {
	s := topicServer(t)
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-a", CallerID: "alice"})

	// The owner edits her own session without any permission; no session header = a person.
	resp := patchSession(t, s, "tok-alice", "sess-a", "", map[string]any{"topic": "gofer 开发", "scope": []string{"inhere-tools/gofer/", "./docs"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner PATCH = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var v sessionView
	decode(t, resp, &v)
	if v.Topic != "gofer 开发" || v.TopicSource != "human" || strings.Join(v.Scope, ",") != "inhere-tools/gofer,docs" {
		t.Fatalf("view after a person's PATCH = %+v", v)
	}

	// The header naming the target session itself = the session's own word.
	resp = patchSession(t, s, "tok-alice", "sess-a", "sess-a", map[string]any{"topic": "自己设的"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("self PATCH = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	decode(t, resp, &v)
	if v.TopicSource != "self" || v.Topic != "自己设的" || len(v.Scope) != 2 {
		t.Fatalf("view after a self PATCH = %+v (scope must be kept when omitted)", v)
	}

	// A header naming ANOTHER session does not make it "self".
	resp = patchSession(t, s, "tok-alice", "sess-a", "sess-other", map[string]any{"topic": "x"})
	decode(t, resp, &v)
	if v.TopicSource != "human" {
		t.Fatalf("a foreign session header gave source %q, want human", v.TopicSource)
	}

	// Someone else's session needs session.manage.others.
	resp = patchSession(t, s, "tok-bob", "sess-a", "", map[string]any{"topic": "bob"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("other's PATCH without the permission = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	s.setDBGrants(grantsFor([]string{authz.PermSessionManageOthers}))
	resp = patchSession(t, s, "tok-bob", "sess-a", "", map[string]any{"topic": "bob 改的"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("other's PATCH with the permission = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	decode(t, resp, &v)
	if v.Topic != "bob 改的" || v.TopicSource != "human" {
		t.Fatalf("view after a permitted edit = %+v", v)
	}
	s.setDBGrants(nil)

	// A worker token and a job credential are refused.
	resp = patchSession(t, s, "tok-worker", "sess-a", "", map[string]any{"topic": "w"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("worker PATCH = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	jobTok := seedJobToken(t, s, "job-topic", jobstore.JobCredentialMember, "")
	resp = patchSession(t, s, jobTok, "sess-a", "", map[string]any{"topic": "j"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("job credential PATCH = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Nothing the refused requests tried was stored.
	got, _, _ := s.jobs.Meta().GetAgentSession("sess-a")
	if got.Topic != "bob 改的" {
		t.Fatalf("stored topic = %q after the refused requests", got.Topic)
	}
}

func TestPatchSessionTopicRejections(t *testing.T) {
	s := topicServer(t)
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-a", CallerID: "alice"})
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-ended", CallerID: "alice", State: jobstore.SessionEnded})

	for name, tc := range map[string]struct {
		sid  string
		body any
		want int
	}{
		"absolute scope":     {"sess-a", map[string]any{"scope": []string{"/etc"}}, http.StatusBadRequest},
		"windows absolute":   {"sess-a", map[string]any{"scope": []string{`C:\work`}}, http.StatusBadRequest},
		"dotdot scope":       {"sess-a", map[string]any{"scope": []string{"a/../../b"}}, http.StatusBadRequest},
		"project root scope": {"sess-a", map[string]any{"scope": []string{"."}}, http.StatusBadRequest},
		"empty scope item":   {"sess-a", map[string]any{"scope": []string{" "}}, http.StatusBadRequest},
		"nothing to set":     {"sess-a", map[string]any{}, http.StatusBadRequest},
		"control in topic":   {"sess-a", map[string]any{"topic": "a\x01b"}, http.StatusBadRequest},
		"topic too long":     {"sess-a", map[string]any{"topic": strings.Repeat("长", 201)}, http.StatusBadRequest},
		"ended session":      {"sess-ended", map[string]any{"topic": "x"}, http.StatusConflict},
		"unknown session":    {"sess-nope", map[string]any{"topic": "x"}, http.StatusNotFound},
	} {
		resp := patchSession(t, s, "tok-alice", tc.sid, "", tc.body)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", name, resp.StatusCode, tc.want, readBody(t, resp))
			continue
		}
		resp.Body.Close()
	}
	got, _, _ := s.jobs.Meta().GetAgentSession("sess-a")
	if got.Topic != "" || len(got.Scope) != 0 {
		t.Fatalf("a rejected PATCH stored something: %+v", got)
	}
	ended, _, _ := s.jobs.Meta().GetAgentSession("sess-ended")
	if ended.Topic != "" {
		t.Fatalf("an ended session's topic was edited: %q", ended.Topic)
	}
}

// 主题为空时视图返回推断值（last_cwd 相对项目根 + peer_name），标 inferred，且不落库。
func TestSessionViewInferredTopicIsNotStored(t *testing.T) {
	s := topicServer(t)
	// A session running in the container: its cwd is on the container side of the project.
	seedTopicSession(t, s, jobstore.AgentSession{
		SessionID: "sess-inf", CallerID: "alice", Cwd: topicCtrRoot, PeerName: "gofer-dev",
	})
	// last_cwd is written by the heartbeat, not by registration.
	if _, _, err := s.jobs.Meta().TouchAgentSession("sess-inf", jobstore.SessionHeartbeat{
		Event: "PostToolUse", Cwd: topicCtrRoot + "/inhere-tools/gofer",
	}); err != nil {
		t.Fatal(err)
	}
	// And one on the host side.
	hostRoot := s.projects.Config().Projects["self"].HostPath
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "sess-host", CallerID: "alice", Cwd: hostRoot + "/docs"})

	for _, tc := range []struct{ sid, want string }{
		{"sess-inf", "inhere-tools/gofer · gofer-dev"},
		{"sess-host", "docs"},
	} {
		resp := do(t, s, http.MethodGet, "/v1/sessions/"+tc.sid, "tok-alice", nil)
		var out struct {
			Session sessionView `json:"session"`
		}
		decode(t, resp, &out)
		if out.Session.Topic != tc.want || out.Session.TopicSource != "inferred" || len(out.Session.Scope) != 0 {
			t.Fatalf("%s view = topic %q source %q, want %q inferred", tc.sid, out.Session.Topic, out.Session.TopicSource, tc.want)
		}
		stored, _, _ := s.jobs.Meta().GetAgentSession(tc.sid)
		if stored.Topic != "" || stored.TopicSource != "" || stored.TopicAt != 0 {
			t.Fatalf("%s: the inferred value was persisted: %+v", tc.sid, stored)
		}
	}

	// Once a real topic is set the view returns it, with its source.
	resp := patchSession(t, s, "tok-alice", "sess-inf", "sess-inf", map[string]any{"topic": "真主题"})
	var v sessionView
	decode(t, resp, &v)
	if v.Topic != "真主题" || v.TopicSource != "self" {
		t.Fatalf("after setting: %+v", v)
	}
}

func findSessions(t *testing.T, s *Server, token, query string) []sessionFindView {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/sessions?"+query, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/sessions?%s = %d: %s", query, resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Sessions []sessionFindView `json:"sessions"`
	}
	decode(t, resp, &out)
	return out.Sessions
}

// 先过滤后截断：他人的会话比上限多，也不能挤掉调用方自己的命中。
func TestFindFiltersBeforeLimit(t *testing.T) {
	s := topicServer(t)
	// alice's own match sorts last (id order on equal rank); bob owns more matches than the limit.
	seedTopicSession(t, s, jobstore.AgentSession{SessionID: "zz-alice-own", CallerID: "alice", State: jobstore.SessionRunning})
	for i := 0; i < 5; i++ {
		seedTopicSession(t, s, jobstore.AgentSession{SessionID: fmt.Sprintf("bob-%d", i), CallerID: "bob", State: jobstore.SessionRunning})
	}
	setTopic := func(sid, topic string) {
		topic2 := topic
		if _, err := s.relay.SetTopic(sid, &topic2, nil, jobstore.SessionTopicHuman); err != nil {
			t.Fatal(err)
		}
	}
	setTopic("zz-alice-own", "deploy notes")
	for i := 0; i < 5; i++ {
		setTopic(fmt.Sprintf("bob-%d", i), "deploy pipeline")
	}

	got := findSessions(t, s, "tok-alice", "q=deploy&limit=2")
	if len(got) != 1 || got[0].SessionID != "zz-alice-own" {
		t.Fatalf("alice without session.read.others found %d rows (%v), want only her own", len(got), ids(got))
	}

	// With the permission she sees everyone's, newest first among equals, cut to the limit.
	s.setDBGrants(grantsFor([]string{authz.PermSessionReadOthers}))
	got = findSessions(t, s, "tok-alice", "q=deploy&limit=2")
	if len(got) != 2 || strings.HasPrefix(got[0].SessionID, "zz-") || strings.HasPrefix(got[1].SessionID, "zz-") {
		t.Fatalf("with the permission: %v, want two of bob's rows", ids(got))
	}
}

func ids(v []sessionFindView) []string {
	out := make([]string, 0, len(v))
	for _, x := range v {
		out = append(out, x.SessionID)
	}
	return out
}

func TestFindHitsOfflineAndDeliverableStates(t *testing.T) {
	s := topicServer(t)
	for sid, st := range map[string]string{
		"s-run": jobstore.SessionRunning, "s-off": jobstore.SessionOffline,
		"s-end": jobstore.SessionEnded, "s-hand": jobstore.SessionHandedOff,
	} {
		seedTopicSession(t, s, jobstore.AgentSession{SessionID: sid, CallerID: "alice", State: jobstore.SessionRunning})
		topic, scope := "mailer work", []string{"inhere-tools/gofer"}
		if _, err := s.relay.SetTopic(sid, &topic, &scope, jobstore.SessionTopicHuman); err != nil {
			t.Fatal(err)
		}
		if _, err := s.jobs.Meta().SetSessionState(sid, st); err != nil {
			t.Fatal(err)
		}
	}

	// Only the deliverable states are found; the offline one is flagged and ranks after the live one.
	got := findSessions(t, s, "tok-alice", "q=mailer")
	if strings.Join(ids(got), ",") != "s-run,s-off" {
		t.Fatalf("found %v, want [s-run s-off]", ids(got))
	}
	if got[0].MaybeOffline || !got[1].MaybeOffline {
		t.Fatalf("maybe_offline = %v / %v, want false / true", got[0].MaybeOffline, got[1].MaybeOffline)
	}
	if len(got[0].Hits) != 1 || got[0].Hits[0].Kind != "topic" || got[0].Hits[0].Source != "human" {
		t.Fatalf("hits = %+v, want one human topic hit", got[0].Hits)
	}

	// An absolute path under the session's project root is turned into a project-relative one;
	// a scope that is an ancestor of the path is a strong hit.
	hostRoot := s.projects.Config().Projects["self"].HostPath
	got = findSessions(t, s, "tok-alice", "path="+hostRoot+"/inhere-tools/gofer/internal/httpapi")
	if len(got) != 2 || got[0].Hits[0].Kind != "scope" || got[0].Hits[0].Match != "inhere-tools/gofer" {
		t.Fatalf("path find = %+v", got)
	}
	// A path that is an ancestor of the scope is only a weak hit.
	got = findSessions(t, s, "tok-alice", "path=inhere-tools")
	if len(got) != 2 || got[0].Hits[0].Kind != "scope_weak" {
		t.Fatalf("weak path find = %+v", got)
	}
	// Neither parameter keeps the plain list; no match is an empty list, not an error.
	if got = findSessions(t, s, "tok-alice", "q=zzz-nothing"); len(got) != 0 {
		t.Fatalf("unmatched query returned %v", ids(got))
	}
}

// The register and heartbeat answers carry the topic (the SessionStart hook decides on them
// whether to remind the agent): inferred while nothing is set, the real one after.
func TestRegisterAndHeartbeatAnswersCarryTopic(t *testing.T) {
	s := topicServer(t)
	resp := do(t, s, http.MethodPost, "/v1/sessions", "tok-alice", map[string]any{
		"session_id": "sess-reg", "agent": "claude", "project_key": "self", "cwd": topicCtrRoot + "/docs", "peer_name": "reg-peer",
	})
	var reg sessionView
	decode(t, resp, &reg)
	if reg.Topic == "" || reg.TopicSource != "inferred" {
		t.Fatalf("register answer = topic %q source %q, want an inferred topic", reg.Topic, reg.TopicSource)
	}

	if r := patchSession(t, s, "tok-alice", "sess-reg", "sess-reg", map[string]any{"topic": "docs work", "scope": []string{"docs"}}); r.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", r.StatusCode)
	}
	resp = do(t, s, http.MethodPost, "/v1/sessions/sess-reg/heartbeat", "tok-alice", map[string]any{"event": "UserPromptSubmit"})
	var hb sessionView
	decode(t, resp, &hb)
	if hb.Topic != "docs work" || hb.TopicSource != "self" || len(hb.Scope) != 1 {
		t.Fatalf("heartbeat answer = %+v", hb)
	}
	// Re-registering (the next SessionStart) keeps it.
	resp = do(t, s, http.MethodPost, "/v1/sessions", "tok-alice", map[string]any{
		"session_id": "sess-reg", "agent": "claude", "project_key": "self", "cwd": topicCtrRoot + "/docs",
	})
	decode(t, resp, &reg)
	if reg.Topic != "docs work" || reg.TopicSource != "self" {
		t.Fatalf("re-register answer = %+v", reg)
	}
}
