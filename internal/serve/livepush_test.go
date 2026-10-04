package serve

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
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
	wireLivePush(ph, st, nil, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		b, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("no %s frame on %s: %v", typ, topic, err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m["t"] == typ && m["topic"] == topic {
			return m
		}
	}
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
	evt := expectFrame(t, c, "evt", "job:j-1")
	if evt["data"].(map[string]any)["status"] != "running" {
		t.Fatalf("job evt=%v", evt)
	}
	inval := expectFrame(t, c, "inval", "jobs")
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
