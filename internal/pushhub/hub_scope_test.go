package pushhub

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/wait"
)

// only lists the projects of a test's scope, like a caller's ceiling does.
func only(projects ...string) func(string) bool {
	set := make(map[string]struct{}, len(projects))
	for _, p := range projects {
		set[p] = struct{}{}
	}
	return func(p string) bool {
		_, ok := set[p]
		return ok
	}
}

// collector drains a connection in the background so a test can assert on what arrived
// (and on what did not) without blocking on Next.
type collector struct {
	mu     sync.Mutex
	frames []map[string]any
}

func collect(t *testing.T, c *Conn) *collector {
	t.Helper()
	col := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			b, err := c.Next(ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			col.mu.Lock()
			col.frames = append(col.frames, m)
			col.mu.Unlock()
		}
	}()
	return col
}

// jobIDs merges the payloads of every `jobs` invalidation received so far.
func (c *collector) jobIDs() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for _, f := range c.frames {
		if f["t"] != "inval" || f["topic"] != TopicJobs {
			continue
		}
		data, _ := f["data"].(map[string]any)
		list, _ := data["jobs"].([]any)
		for _, it := range list {
			e, _ := it.(map[string]any)
			id, _ := e["id"].(string)
			status, _ := e["status"].(string)
			out[id] = status
		}
	}
	return out
}

// topics lists the topics of every frame received so far.
func (c *collector) topics() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.frames))
	for _, f := range c.frames {
		if t, ok := f["topic"].(string); ok {
			out = append(out, t)
		}
	}
	return out
}

// snapFrames returns the raw body of every snapshot frame on topic, in arrival order.
func (c *collector) snapFrames(topic string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.frames))
	for _, f := range c.frames {
		if f["t"] == "snap" && f["topic"] == topic {
			b, err := json.Marshal(f["data"])
			if err == nil {
				out = append(out, string(b))
			}
		}
	}
	return out
}

func (c *collector) count(typ, topic string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, f := range c.frames {
		if f["t"] == typ && f["topic"] == topic {
			n++
		}
	}
	return n
}

// A restricted connection hears only the `jobs` invalidation entries of its own projects
// while an unrestricted one hears every entry (gofer-7798 phase 2, decision 1).
func TestScopedJobsInvalidationIsFiltered(t *testing.T) {
	h := New(Options{InvalInterval: 5 * time.Millisecond})
	unres, err := h.Register("u")
	if err != nil {
		t.Fatal(err)
	}
	defer unres.Close()
	scoped, err := h.RegisterScoped("a", ConnScope{Key: "proj-a", Scope: only("proj-a")})
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()

	unres.Subscribe([]string{TopicJobs}, nil)
	scoped.Subscribe([]string{TopicJobs}, nil)
	uc, sc := collect(t, unres), collect(t, scoped)

	h.NotifyJobProject("j-a", "running", "proj-a")
	wait.For(t, wait.Timeout(t, 5*time.Second), "the unrestricted connection to hear j-a", func() (bool, any) {
		return uc.jobIDs()["j-a"] != "", uc.jobIDs()
	})
	h.NotifyJobProject("j-b", "running", "proj-b")
	wait.For(t, wait.Timeout(t, 5*time.Second), "the unrestricted connection to hear both jobs", func() (bool, any) {
		ids := uc.jobIDs()
		return ids["j-a"] != "" && ids["j-b"] != "", ids
	})
	wait.For(t, wait.Timeout(t, 5*time.Second), "the restricted connection to hear j-a", func() (bool, any) {
		return sc.jobIDs()["j-a"] != "", sc.jobIDs()
	})

	if ids := sc.jobIDs(); ids["j-b"] != "" {
		t.Fatalf("restricted connection heard another project's job: %v", ids)
	}
	if status := uc.jobIDs()["j-b"]; status != "running" {
		t.Fatalf("unrestricted status for j-b = %q, want running", status)
	}
}

// The unrestricted frame is byte for byte the frame the hub emitted before connections
// could carry a scope (the regression the plan asks for).
func TestUnrestrictedJobsInvalidationFrameUnchanged(t *testing.T) {
	h := New(Options{InvalInterval: 5 * time.Millisecond})
	c, err := h.Register("u")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Subscribe([]string{TopicJobs}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 5*time.Second))
	defer cancel()
	h.NotifyJobProject("j9", "running", "proj-a")
	nextFrame(t, c) // hello
	b, err := c.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	const want = `{"data":{"jobs":[{"id":"j9","status":"running"}]},"t":"inval","topic":"jobs"}`
	if string(b) != want {
		t.Fatalf("unrestricted jobs inval =\n%s\nwant\n%s", b, want)
	}
}

// A change nobody can attribute to a project is a global invalidation: a restricted
// connection still hears it (it refetches, and the REST call it makes is scoped).
func TestScopedConnectionHearsUnattributedInvalidation(t *testing.T) {
	h := New(Options{InvalInterval: 5 * time.Millisecond})
	scoped, err := h.RegisterScoped("a", ConnScope{Key: "proj-a", Scope: only("proj-a")})
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	scoped.Subscribe([]string{TopicSessions}, nil)
	col := collect(t, scoped)

	h.Notify(TopicSessions)
	wait.For(t, wait.Timeout(t, 5*time.Second), "the restricted connection to hear the global invalidation", func() (bool, any) {
		return col.count("inval", TopicSessions) == 1, col.topics()
	})
}

// An invalidation that names a project outside the connection's scope is not delivered
// as such: only the coalesced burst's in-scope projects make it hear anything.
func TestScopedInvalidationSkipsOtherProjects(t *testing.T) {
	h := New(Options{InvalInterval: 5 * time.Millisecond})
	unres, err := h.Register("u")
	if err != nil {
		t.Fatal(err)
	}
	defer unres.Close()
	scoped, err := h.RegisterScoped("a", ConnScope{Key: "proj-a", Scope: only("proj-a")})
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	unres.Subscribe([]string{TopicPlans}, nil)
	scoped.Subscribe([]string{TopicPlans}, nil)
	uc, sc := collect(t, unres), collect(t, scoped)

	h.NotifyProject(TopicPlans, "proj-b")
	wait.For(t, wait.Timeout(t, 5*time.Second), "the unrestricted connection to hear proj-b", func() (bool, any) {
		return uc.count("inval", TopicPlans) == 1, nil
	})
	if n := sc.count("inval", TopicPlans); n != 0 {
		t.Fatalf("restricted connection heard %d proj-b invalidations", n)
	}

	h.NotifyProject(TopicPlans, "proj-a")
	wait.For(t, wait.Timeout(t, 5*time.Second), "the restricted connection to hear proj-a", func() (bool, any) {
		return sc.count("inval", TopicPlans) == 1, nil
	})
}

// `job:<id>` increments reach a restricted connection only for a job whose project it
// admits — resolved from the publisher when it knows it, else from the subscription.
func TestScopedJobTopicFiltered(t *testing.T) {
	h := New(Options{InvalInterval: 5 * time.Millisecond})
	var resolved atomic.Value
	h.SetJobProject(func(id string) (string, bool) {
		m, _ := resolved.Load().(map[string]string)
		p, ok := m[id]
		return p, ok && p != ""
	})
	_ = resolved.Swap(map[string]string{"j-in": "proj-a", "j-out": "proj-b"})

	unres, _ := h.Register("u")
	defer unres.Close()
	scoped, err := h.RegisterScoped("a", ConnScope{Key: "proj-a", Scope: only("proj-a")})
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	unres.Subscribe([]string{JobTopic("j-in"), JobTopic("j-out")}, nil)
	scoped.Subscribe([]string{JobTopic("j-in"), JobTopic("j-out")}, nil)
	uc, sc := collect(t, unres), collect(t, scoped)

	h.PublishJobEvent("j-out", JobEvent{Seq: 1, Type: "job.running"})
	h.PublishJobKind("j-out", "interaction")
	h.NotifyJobProject("j-out", "running", "proj-b")
	h.PublishJobEvent("j-in", JobEvent{Seq: 2, Type: "job.running"})

	wait.For(t, wait.Timeout(t, 5*time.Second), "the unrestricted connection to hear both jobs", func() (bool, any) {
		return uc.count("evt", JobTopic("j-in")) >= 1 && uc.count("evt", JobTopic("j-out")) >= 3, uc.topics()
	})
	wait.For(t, wait.Timeout(t, 5*time.Second), "the restricted connection to hear j-in", func() (bool, any) {
		return sc.count("evt", JobTopic("j-in")) == 1, sc.topics()
	})
	if got := sc.count("evt", JobTopic("j-out")); got != 0 {
		t.Fatalf("restricted connection heard %d frames of another project's job", got)
	}
}

// Snapshot topics are computed once per project restriction: connections sharing a key
// share one frame, and an unrestricted connection keeps the single shared frame it had.
func TestScopedSnapshotPerScopeAndShared(t *testing.T) {
	h := New(Options{StatsInterval: 5 * time.Millisecond})
	var calls atomic.Int64
	h.SetSnapshot(TopicStats, func() (any, error) {
		return map[string]any{"global": calls.Add(1)}, nil
	})
	h.SetScopedSnapshot(TopicStats, func(scope func(project string) bool) (any, error) {
		// Stand-in for httpapi's builder: it would only add the rows of scope's projects.
		return map[string]any{"scoped_a": scope("proj-a"), "n": calls.Add(1)}, nil
	})

	unres, _ := h.Register("u")
	defer unres.Close()
	a1, _ := h.RegisterScoped("a1", ConnScope{Key: "proj-a", Scope: only("proj-a")})
	defer a1.Close()
	a2, _ := h.RegisterScoped("a2", ConnScope{Key: "proj-a", Scope: only("proj-a")})
	defer a2.Close()
	uc, c1, c2 := collect(t, unres), collect(t, a1), collect(t, a2)

	unres.Subscribe([]string{TopicStats}, nil)
	base := calls.Load()
	a1.Subscribe([]string{TopicStats}, nil)
	wait.For(t, wait.Timeout(t, 5*time.Second), "the restricted connection's snapshot", func() (bool, any) {
		return len(c1.snapFrames(TopicStats)) == 1, nil
	})
	if got := calls.Load() - base; got != 1 {
		t.Fatalf("scoped snapshot computed %d times for one subscription, want 1", got)
	}

	// A second connection of the same restriction costs no computation: it gets the
	// cached frame of its own scope, not the unrestricted one.
	base = calls.Load()
	a2.Subscribe([]string{TopicStats}, nil)
	wait.For(t, wait.Timeout(t, 5*time.Second), "the second restricted connection's snapshot", func() (bool, any) {
		return len(c2.snapFrames(TopicStats)) == 1, nil
	})
	if got := calls.Load() - base; got != 0 {
		t.Fatalf("snapshot recomputed for a connection of the same scope: %d calls", got)
	}
	s1, s2 := c1.snapFrames(TopicStats), c2.snapFrames(TopicStats)
	if s1[0] != s2[0] {
		t.Fatalf("connections of one scope got different frames:\n%s\n%s", s1[0], s2[0])
	}

	// One recomputation per distinct scope, and no cross-scope frame.
	base = calls.Load()
	h.Notify(TopicStats)
	wait.For(t, wait.Timeout(t, 5*time.Second), "both scopes to be told stats changed", func() (bool, any) {
		return len(c1.snapFrames(TopicStats)) >= 2 && len(uc.snapFrames(TopicStats)) >= 2, nil
	})
	if got := calls.Load() - base; got > 2 {
		t.Fatalf("stats recomputed %d times for two scopes (want at most 2: one unrestricted, one scoped)", got)
	}
	unrestricted := uc.snapFrames(TopicStats)
	if unrestricted[len(unrestricted)-1] == c1.snapFrames(TopicStats)[0] && len(unrestricted) > 0 {
		t.Fatal("a scoped connection received the unrestricted snapshot frame")
	}
}

// A restricted connection gets no snapshot of a topic that only has an unrestricted
// source: the hub never falls back to the payload of every project (fail closed).
func TestScopedSnapshotWithoutScopedSourceIsAbsent(t *testing.T) {
	h := New(Options{})
	h.SetSnapshot(TopicStats, func() (any, error) { return map[string]any{"global": true}, nil })
	scoped, err := h.RegisterScoped("a", ConnScope{Key: "proj-a", Scope: only("proj-a")})
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	scoped.Subscribe([]string{TopicStats}, nil)
	nextFrame(t, scoped) // hello

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if b, err := scoped.Next(ctx); err == nil {
		t.Fatalf("a scoped connection received a snapshot without a scoped source: %s", b)
	}
}

// A scoped connection without a fingerprint must still never share another restriction's
// cached frame.
func TestScopedConnectionWithoutKeyDoesNotShare(t *testing.T) {
	h := New(Options{})
	c, err := h.RegisterScoped("a", ConnScope{Scope: only("proj-a")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	d, err := h.RegisterScoped("b", ConnScope{Scope: only("proj-b")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if c.scope.Key == "" || d.scope.Key == "" || c.scope.Key == d.scope.Key {
		t.Fatalf("scoped connections without a key got %q / %q", c.scope.Key, d.scope.Key)
	}
	if got := snapKey(TopicStats, c.scope); got == snapKey(TopicStats, d.scope) {
		t.Fatalf("two restrictions share the cache key %q", got)
	}
}
