package sessionrelay

import (
	"context"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// gofer-vin1: a session whose cwd is outside every configured project has an EMPTY
// project key. The relay must not refuse it (the old `no_project` gate) and must not
// hand an empty project to a job: the host resolves it to the execution machine's
// default workspace, while the relay only has to stop treating "no project" as a wall.

// cwdMessenger records each one-shot messenger submission's project and cwd, and answers
// 已发送.
type cwdMessenger struct {
	projects []string
	cwds     []string
}

func (m *cwdMessenger) SubmitMessenger(projectKey, _, cwd string, _ []string, _, _ string) (string, error) {
	m.projects = append(m.projects, projectKey)
	m.cwds = append(m.cwds, cwd)
	return "job-msgr", nil
}

func (m *cwdMessenger) MessengerJob(string) (bool, string, int, string, error) {
	return true, "done", 0, "已发送", nil
}

// projectlessSession registers a session whose cwd belongs to no project (the shape the
// hooks report for a CLI started outside every configured project).
func projectlessSession(t *testing.T, s *Service, sid string) {
	t.Helper()
	_, err := s.Register(RegisterInput{SessionID: sid, Agent: "claude", ProjectKey: "",
		Runner: "w-x", Cwd: "/outside/every/project", PeerName: "peer-1", PeerMessaging: true,
		Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.Heartbeat(sid, HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
}

// TestWakeMessengerForAProjectlessSession: the messenger is still dispatched, with the
// EMPTY project the host resolves to the default workspace — and in the project root
// rather than in a directory outside every project.
func TestWakeMessengerForAProjectlessSession(t *testing.T) {
	s := newSvc(t)
	s.ConfigureMessaging(true, "claude", 2*time.Second, 0)
	m := &cwdMessenger{}
	s.SetMessenger(m)
	projectlessSession(t, s, "sid-noproj-wake")

	res, err := s.Wake(context.Background(), "sid-noproj-wake", "WAKE-TEXT", "gofer-mail", "gofer-mail")
	assert.NoErr(t, err)
	assert.Eq(t, PathMessenger, res.Path)
	assert.Len(t, m.projects, 1)
	assert.Eq(t, "", m.projects[0]) // the host owns the fallback (config.DefaultProjectKey)
	assert.Eq(t, ".", m.cwds[0])    // never the session's cwd outside every project
}

// TestCheckDeliverableForAProjectlessSession: the pre-send check no longer answers
// no_project — the very refusal that reached the user as 「无法派发传话人」.
func TestCheckDeliverableForAProjectlessSession(t *testing.T) {
	s := newSvc(t)
	s.ConfigureMessaging(true, "claude", 2*time.Second, 0)
	s.SetMessenger(&cwdMessenger{})
	projectlessSession(t, s, "sid-noproj-check")

	_, err := s.Heartbeat("sid-noproj-check", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)
	assert.NoErr(t, s.CheckDeliverable("sid-noproj-check"))
}

// TestTakeoverOfAProjectlessSessionSaysWhy: path B cannot continue a session that has no
// project root to reopen it in, and it says so — instead of claiming the agent has no
// resume template (it does; only the project is unknown).
func TestTakeoverOfAProjectlessSessionSaysWhy(t *testing.T) {
	s := newSvc(t)
	to := &fakeTakeoverer{res: TakeoverResult{JobID: "job-takeover-x"}}
	s.SetTakeoverer(to)
	s.SetInjector(&fakeInjector{})
	_, err := s.Register(RegisterInput{SessionID: "sid-noproj-takeover", Agent: "claude",
		Runner: "w-claude", Cwd: "/outside/every/project", Event: EventSessionStart})
	assert.NoErr(t, err)
	_, err = s.Heartbeat("sid-noproj-takeover", HeartbeatInput{Event: EventStop})
	assert.NoErr(t, err)

	_, err = s.Deliver(context.Background(), "sid-noproj-takeover", "carry on", "alice", "alice", true)
	assert.Eq(t, ReasonNoProject, DeliverReason(err))
	assert.Len(t, to.calls, 0) // nothing was planned for an unknown project
	assert.Len(t, to.reqs, 0)
}
