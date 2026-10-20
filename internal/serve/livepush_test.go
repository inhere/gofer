package serve

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/work"
)

func liveFixture(t *testing.T) (*pushhub.Hub, *jobstore.Store, *pushhub.Conn) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ph := pushhub.New(pushhub.Options{
		InvalInterval: 10 * time.Millisecond, PendingInterval: 10 * time.Millisecond,
		StatsInterval: 10 * time.Millisecond, SessionsInterval: 10 * time.Millisecond,
	})
	wireLivePush(ph, st, nil, nil, nil)
	c, err := ph.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return ph, st, c
}

// expectFrame waits for a frame of type typ on topic (other frames are skipped).
func expectFrame(t *testing.T, c *pushhub.Conn, typ, topic string) map[string]any {
	t.Helper()
	return expectFrames(t, c, [2]string{typ, topic})[0]
}

// expectFrames waits until one frame of each wanted {type, topic} has arrived, in any
// order, and returns them in the order asked for. Frames on different topics carry no
// ordering promise (the jobs inval fires from the coalescer's goroutine, the job:<id>
// evt is broadcast inline), so a test watching two topics must not wait for them one
// after the other: the skipped frame would be lost.
func expectFrames(t *testing.T, c *pushhub.Conn, want ...[2]string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()
	got := make([]map[string]any, len(want))
	for left := len(want); left > 0; {
		b, err := c.Next(ctx)
		if err != nil {
			for i, w := range want {
				if got[i] == nil {
					t.Fatalf("no %s frame on %s: %v", w[0], w[1], err)
				}
			}
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		for i, w := range want {
			if got[i] == nil && m["t"] == w[0] && m["topic"] == w[1] {
				got[i] = m
				left--
				break
			}
		}
	}
	return got
}

func TestPersistPublishesJobs(t *testing.T) {
	_, st, c := liveFixture(t)
	c.Subscribe([]string{pushhub.TopicJobs, pushhub.JobTopic("j-1")}, nil)
	now := time.Now().Unix()
	if err := st.UpsertJob(jobstore.JobRecord{
		ID: "j-1", ProjectKey: "p", Agent: "exec", Runner: "local", Status: "running",
		ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	frames := expectFrames(t, c, [2]string{"evt", "job:j-1"}, [2]string{"inval", "jobs"})
	evt, inval := frames[0], frames[1]
	if evt["data"].(map[string]any)["status"] != "running" {
		t.Fatalf("job evt=%v", evt)
	}
	list := inval["data"].(map[string]any)["jobs"].([]any)
	if list[0].(map[string]any)["id"] != "j-1" || list[0].(map[string]any)["status"] != "running" {
		t.Fatalf("jobs inval=%v", inval)
	}
}

func TestDecisionChangesPublishPending(t *testing.T) {
	_, st, c := liveFixture(t)
	c.Subscribe([]string{pushhub.TopicPending}, nil)
	d := &jobstore.PlanDecision{ID: "d1", Title: "t", Question: "q"}
	steps := []struct {
		name string
		do   func()
	}{
		{"insert", func() { _ = st.InsertDecision(d) }},
		{"ack", func() { _, _ = st.AckDecision("d1", "alice") }},
		{"unack", func() { _, _ = st.UnackDecision("d1") }},
		{"answer", func() { _, _ = st.AnswerDecision("d1", "yes", "alice") }},
	}
	for _, s := range steps {
		s.do()
		expectFrame(t, c, "inval", "pending")
		time.Sleep(30 * time.Millisecond) // let the coalescer settle between steps
	}
	d2 := &jobstore.PlanDecision{ID: "d2", Title: "t", Question: "q"}
	_ = st.InsertDecision(d2)
	expectFrame(t, c, "inval", "pending")
	time.Sleep(30 * time.Millisecond)
	_, _ = st.ReleaseDecision("d2", "user_returned")
	expectFrame(t, c, "inval", "pending")
}

func TestDecisionExpirySweepPublishes(t *testing.T) {
	_, st, c := liveFixture(t)
	// A decision already past its deadline, inserted without anybody reading it.
	if err := st.InsertDecision(&jobstore.PlanDecision{
		ID: "old", Title: "t", Question: "q", TimeoutSec: 2, AskedAt: time.Now().Unix() - 100,
	}); err != nil {
		t.Fatal(err)
	}
	c.Subscribe([]string{pushhub.TopicPending}, nil)
	stop := make(chan struct{})
	defer close(stop)
	startDecisionExpiryLoop(st, 20*time.Millisecond, stop)
	expectFrame(t, c, "inval", "pending")
	got, ok, err := st.GetDecision("old")
	if err != nil || !ok || got.State != jobstore.DecisionExpired {
		t.Fatalf("decision state=%v ok=%v err=%v", got.State, ok, err)
	}
}

// W1: a work-item write reaches a `work` subscriber as an inval, and a session write
// asks the work service for a status re-sync.
func TestWorkChangesPublishWorkTopicAndSessionsMarkDirty(t *testing.T) {
	_, st, c := liveFixture(t)
	c.Subscribe([]string{pushhub.TopicWork}, nil)
	if _, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, c, "inval", pushhub.TopicWork)
}

func TestSessionWritesMarkWorkServiceDirty(t *testing.T) {
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws := work.New(st)
	t.Cleanup(func() { _ = ws.Close() }) // LIFO: stops Run before the store closes
	wireLivePush(pushhub.New(pushhub.Options{}), st, nil, nil, ws)
	a, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "s1", Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", Source: jobstore.WorkOriginAuto, SessionIDs: []string{a.SessionID}})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go ws.Run(stop)
	time.Sleep(50 * time.Millisecond) // let Run consume the dirty flags raised by the setup writes
	if ok, err := st.SetSessionState("s1", jobstore.SessionWaitingReply); err != nil || !ok {
		t.Fatalf("SetSessionState: %v ok=%v", err, ok)
	}
	wait.For(t, 3*time.Second, "work item to move to needs_me after the session started waiting", func() (bool, any) {
		got, _, _ := st.GetWorkItem(w.ID)
		return got.Status == jobstore.WorkNeedsMe, got.Status
	})
}

// drainFrames reads a connection in the background; the returned func snapshots what has
// arrived so far (the goroutine stops before the store closes: it is registered after it).
func drainFrames(t *testing.T, c *pushhub.Conn) func() []map[string]any {
	t.Helper()
	var mu sync.Mutex
	var frames []map[string]any
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
			mu.Lock()
			frames = append(frames, m)
			mu.Unlock()
		}
	}()
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]any, len(frames))
		copy(out, frames)
		return out
	}
}

// jobsIn merges the `jobs` invalidation payloads of the given frames.
func jobsIn(frames []map[string]any) map[string]string {
	out := map[string]string{}
	for _, f := range frames {
		if f["t"] != "inval" || f["topic"] != pushhub.TopicJobs {
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

// wireLivePush hands each write's project to the hub, so a restricted push connection
// hears only the invalidations of its own projects (gofer-7798 phase 2).
func TestLivePushScopeFiltersInvalidations(t *testing.T) {
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ph := pushhub.New(pushhub.Options{
		InvalInterval: 10 * time.Millisecond, PendingInterval: 10 * time.Millisecond,
		StatsInterval: 10 * time.Millisecond, SessionsInterval: 10 * time.Millisecond,
	})
	wireLivePush(ph, st, nil, nil, nil)
	unres, err := ph.Register("u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unres.Close)
	scoped, err := ph.RegisterScoped("a", pushhub.ConnScope{Key: "proj-a", Scope: func(p string) bool { return p == "proj-a" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scoped.Close)
	unres.Subscribe([]string{pushhub.TopicJobs}, nil)
	scoped.Subscribe([]string{pushhub.TopicJobs}, nil)
	uFrames := drainFrames(t, unres)
	sFrames := drainFrames(t, scoped)

	now := time.Now().Unix()
	upsert := func(id, project string) {
		t.Helper()
		err := st.UpsertJob(jobstore.JobRecord{
			ID: id, ProjectKey: project, Agent: "exec", Runner: "local", Status: "running",
			ResultDir: t.TempDir(), StartedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatalf("UpsertJob %s: %v", id, err)
		}
	}

	upsert("j-b", "proj-b")
	wait.For(t, wait.Timeout(t, 10*time.Second), "the unrestricted connection to hear j-b", func() (bool, any) {
		return jobsIn(uFrames())["j-b"] != "", jobsIn(uFrames())
	})
	upsert("j-a", "proj-a")
	wait.For(t, wait.Timeout(t, 10*time.Second), "the restricted connection to hear j-a", func() (bool, any) {
		return jobsIn(sFrames())["j-a"] != "", jobsIn(sFrames())
	})
	if ids := jobsIn(sFrames()); ids["j-b"] != "" {
		t.Fatalf("restricted connection heard another project's job: %v", ids)
	}
}

// A worker enrollment write reaches a `worker-enrollments` subscriber as a payload-free inval.
func TestWorkerEnrollmentChangePublishesTopic(t *testing.T) {
	_, st, c := liveFixture(t)
	c.Subscribe([]string{pushhub.TopicWorkerEnrollments}, nil)
	_, err := st.InsertWorkerEnrollment(jobstore.WorkerEnrollment{
		ID: "e1", SecretHash: "h", PairingCode: "123456", CreatedAt: time.Now().Unix(), DecideUntil: time.Now().Unix() + 60,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := expectFrame(t, c, "inval", pushhub.TopicWorkerEnrollments)
	if d, _ := m["data"].(map[string]any); len(d) != 0 {
		t.Errorf("an invalidation carries no payload, got %v", m)
	}
}
