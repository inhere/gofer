package sessionrelay

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

var findRoots = map[string][]string{"gofer": {"/host/ws", "/ctr/ws"}}

func findRootsFn(key string) []string { return findRoots[key] }

type findSeed struct {
	sid, project, state, peer, topic string
	scope                            []string
	source                           string
	cwd                              string
	seen                             int64
}

func seedFind(t *testing.T, s *Service, rows ...findSeed) {
	t.Helper()
	for _, r := range rows {
		if _, err := s.store.UpsertAgentSession(jobstore.AgentSession{
			SessionID: r.sid, Agent: "claude", ProjectKey: r.project, State: jobstore.SessionRunning,
			PeerName: r.peer, Cwd: r.cwd,
		}); err != nil {
			t.Fatal(err)
		}
		if r.topic != "" || r.scope != nil {
			topic, scope := r.topic, r.scope
			src := r.source
			if src == "" {
				src = jobstore.SessionTopicHuman
			}
			if _, _, _, err := s.store.SetSessionTopic(r.sid, &topic, &scope, src); err != nil {
				t.Fatal(err)
			}
		}
		if r.state != "" && r.state != jobstore.SessionRunning {
			if _, err := s.store.SetSessionState(r.sid, r.state); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func resultIDs(rs []FindResult) string {
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.Session.SessionID)
	}
	return strings.Join(ids, ",")
}

// 匹配方向：scope 等于或是查询路径的祖先 = 强命中；查询路径是 scope 的祖先 = 弱命中；兄弟不命中。
func TestFindPathDirection(t *testing.T) {
	s := newSvc(t)
	seedFind(t, s,
		findSeed{sid: "exact", project: "gofer", scope: []string{"a/b"}},
		findSeed{sid: "anc", project: "gofer", scope: []string{"a"}},
		findSeed{sid: "desc", project: "gofer", scope: []string{"a/b/c"}},
		findSeed{sid: "sibling", project: "gofer", scope: []string{"a/bb"}},
		findSeed{sid: "other", project: "gofer", scope: []string{"x"}},
		findSeed{sid: "noscope", project: "gofer"},
	)
	for _, tc := range []struct {
		name, path, want string
		kinds            map[string]string
	}{
		{"query inside scope", "a/b", "anc,exact,desc", map[string]string{"anc": HitScope, "exact": HitScope, "desc": HitScopeWeak}},
		{"query below scope", "a/b/c/d.go", "anc,desc,exact", map[string]string{"anc": HitScope, "exact": HitScope, "desc": HitScope}},
		{"query above scope", "a", "anc,desc,exact,sibling", map[string]string{"anc": HitScope, "exact": HitScopeWeak, "desc": HitScopeWeak, "sibling": HitScopeWeak}},
		{"no relation", "z", "", nil},
		{"absolute host path", "/host/ws/a/b", "anc,exact,desc", map[string]string{"exact": HitScope}},
		{"absolute container path", "/ctr/ws/a/b/", "anc,exact,desc", nil},
		{"absolute path outside the project", "/other/a/b", "", nil},
		{"dot-prefixed", "./a/b", "anc,exact,desc", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Find(FindQuery{Path: tc.path, Roots: findRootsFn})
			if err != nil {
				t.Fatal(err)
			}
			if !sameSet(resultIDs(got), tc.want) {
				t.Fatalf("path %q found %q, want %q", tc.path, resultIDs(got), tc.want)
			}
			for _, r := range got {
				if want, ok := tc.kinds[r.Session.SessionID]; ok && r.Hits[0].Kind != want {
					t.Errorf("%s: hit kind %s, want %s", r.Session.SessionID, r.Hits[0].Kind, want)
				}
			}
		})
	}
}

func sameSet(a, b string) bool {
	split := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, x := range strings.Split(s, ",") {
			if x != "" {
				m[x] = true
			}
		}
		return m
	}
	ma, mb := split(a), split(b)
	if len(ma) != len(mb) {
		return false
	}
	for k := range ma {
		if !mb[k] {
			return false
		}
	}
	return true
}

// 排序：scope 强命中 > scope 弱命中 > 主题 > peer_name > 项目；同等时在线优先。
func TestFindRanking(t *testing.T) {
	s := newSvc(t)
	seedFind(t, s,
		findSeed{sid: "by-project", project: "gofer-proj"},
		findSeed{sid: "by-peer", project: "x", peer: "relay-peer"},
		findSeed{sid: "by-topic", project: "x", topic: "relay work"},
		findSeed{sid: "by-topic-offline", project: "x", topic: "relay docs", state: jobstore.SessionOffline},
		findSeed{sid: "by-topic-inferred", project: "gofer", cwd: "/host/ws/relay-area"},
	)
	got, err := s.Find(FindQuery{Q: "relay", Roots: findRootsFn})
	if err != nil {
		t.Fatal(err)
	}
	// topic (live) > topic (offline, same rank) > inferred topic > peer_name; project does not match "relay".
	if want := "by-topic,by-topic-offline,by-topic-inferred,by-peer"; resultIDs(got) != want {
		t.Fatalf("keyword ranking = %q, want %q (%+v)", resultIDs(got), want, got)
	}
	if got[1].MaybeOffline != true || got[0].MaybeOffline {
		t.Fatalf("maybe_offline flags wrong: %+v", got)
	}
	if got[2].TopicSource != TopicSourceInferred || got[2].Hits[0].Source != TopicSourceInferred {
		t.Fatalf("an inferred match must be marked: %+v", got[2])
	}

	got, err = s.Find(FindQuery{Q: "gofer", Roots: findRootsFn})
	if err != nil {
		t.Fatal(err)
	}
	if resultIDs(got) != "by-topic-inferred,by-project" && resultIDs(got) != "by-project,by-topic-inferred" {
		t.Fatalf("project query = %q", resultIDs(got))
	}
	for _, r := range got {
		if r.Hits[0].Kind != HitProject && r.Hits[0].Kind != HitTopic {
			t.Fatalf("unexpected hit kinds %+v", r.Hits)
		}
	}

	// scope strong beats a topic hit; every keyword term must match.
	seedFind(t, s,
		findSeed{sid: "scoped", project: "x", topic: "unrelated", scope: []string{"a"}},
		findSeed{sid: "topical", project: "x", topic: "path a work"},
	)
	got, _ = s.Find(FindQuery{Q: "", Path: "a/b", Roots: findRootsFn})
	if resultIDs(got) != "scoped" {
		t.Fatalf("path-only find = %q", resultIDs(got))
	}
	got, _ = s.Find(FindQuery{Q: "unrelated work", Roots: findRootsFn})
	if resultIDs(got) != "" {
		t.Fatalf("a term nothing matches must exclude the session, got %q", resultIDs(got))
	}
	// q AND path: both must hold.
	got, _ = s.Find(FindQuery{Q: "unrelated", Path: "a", Roots: findRootsFn})
	if resultIDs(got) != "scoped" {
		t.Fatalf("q+path = %q", resultIDs(got))
	}
	got, _ = s.Find(FindQuery{Q: "work", Path: "a", Roots: findRootsFn})
	if resultIDs(got) != "" {
		t.Fatalf("q+path without a scope hit = %q", resultIDs(got))
	}
}

// 排序在截断之前，且只在可投递状态里找；Visible 在排序前过滤。
func TestFindRanksThenLimitsAndSkipsNonDeliverable(t *testing.T) {
	s := newSvc(t)
	seedFind(t, s,
		findSeed{sid: "ended", project: "x", topic: "deploy", scope: []string{"a"}, state: ""},
		findSeed{sid: "handed", project: "x", topic: "deploy", state: ""},
		findSeed{sid: "p1", project: "x", peer: "deploy"},
		findSeed{sid: "p2", project: "x", peer: "deploy"},
		findSeed{sid: "t1", project: "x", topic: "deploy"},
	)
	for sid, st := range map[string]string{"ended": jobstore.SessionEnded, "handed": jobstore.SessionHandedOff} {
		if _, err := s.store.SetSessionState(sid, st); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Find(FindQuery{Q: "deploy", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resultIDs(got) != "t1" {
		t.Fatalf("limit 1 returned %q: the best hit must survive the cut", resultIDs(got))
	}
	got, _ = s.Find(FindQuery{Q: "deploy", Visible: func(a jobstore.AgentSession) bool { return a.SessionID != "t1" }})
	if !sameSet(resultIDs(got), "p1,p2") {
		t.Fatalf("visible filter result = %q", resultIDs(got))
	}
	if _, err := s.Find(FindQuery{}); err == nil {
		t.Fatal("an empty query must be rejected")
	}
}

func TestInferTopicRootSelection(t *testing.T) {
	roots := []string{"/host/ws", "/ctr/ws", `D:\work\ws`}
	for _, tc := range []struct {
		name string
		a    jobstore.AgentSession
		want string
	}{
		{"container side", jobstore.AgentSession{LastCwd: "/ctr/ws/inhere-tools/gofer"}, "inhere-tools/gofer"},
		{"host side with peer", jobstore.AgentSession{Cwd: "/host/ws/docs", PeerName: "p"}, "docs · p"},
		{"last_cwd wins over cwd", jobstore.AgentSession{Cwd: "/host/ws/a", LastCwd: "/host/ws/b"}, "b"},
		{"at the root: peer only", jobstore.AgentSession{Cwd: "/host/ws", PeerName: "p"}, "p"},
		{"windows drive, any case", jobstore.AgentSession{Cwd: `d:\work\ws\x\y`}, "x/y"},
		{"prefix is not a path prefix", jobstore.AgentSession{Cwd: "/host/ws2/x", PeerName: "p"}, "p"},
		{"outside every root", jobstore.AgentSession{Cwd: "/elsewhere/x"}, ""},
	} {
		if got := InferTopic(tc.a, roots); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeScope(t *testing.T) {
	got, err := NormalizeScope([]string{"a/b/", "./c", "a/b", `d\e`})
	if err != nil || strings.Join(got, ",") != "a/b,c,d/e" {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"/abs", `\abs`, "C:/x", "a/../b", "..", ".", "", "a\x00b"} {
		if _, err := NormalizeScope([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
