package worker

import (
	"context"
	"testing"

	"github.com/inhere/gofer/internal/wsproto"
)

type sessionCommandJobs struct {
	Jobs
	say, end int
}

func (j *sessionCommandJobs) SaySession(string, string) error { j.say++; return nil }
func (j *sessionCommandJobs) EndSession(string) error         { j.end++; return nil }

func TestRemoteSessionSayEndRoundTrip(t *testing.T) {
	jobs := &sessionCommandJobs{}
	cl := &Client{
		workerID: "w1", jobs: jobs,
		jobMap:         map[string]string{"remote-1": "local-1"},
		sessionCmdSeen: map[string]struct{}{},
	}
	cl.handleSessionCommand(context.Background(), wsproto.SessionCommand{JobID: "remote-1", CmdID: "say-1", Action: "say", Prompt: "next"})
	cl.handleSessionCommand(context.Background(), wsproto.SessionCommand{JobID: "remote-1", CmdID: "say-1", Action: "say", Prompt: "next"})
	cl.handleSessionCommand(context.Background(), wsproto.SessionCommand{JobID: "remote-1", CmdID: "end-1", Action: "end"})
	if jobs.say != 1 || jobs.end != 1 {
		t.Fatalf("say=%d end=%d, want one execution each", jobs.say, jobs.end)
	}
}
