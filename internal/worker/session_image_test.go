package worker

import (
	"context"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

type imageSessionCommandJobs struct {
	sessionCommandJobs
	text   string
	images []job.ImageInput
}

func (j *imageSessionCommandJobs) SaySessionBlocks(_, text string, images []job.ImageInput) error {
	j.text, j.images = text, images
	return nil
}

// A v25 say carrying images reaches the local session as image inputs (and not as a
// plain text say); a text-only say still takes the plain path.
func TestRemoteSessionSayForwardsImages(t *testing.T) {
	jobs := &imageSessionCommandJobs{}
	cl := &Client{
		workerID: "w1", jobs: jobs,
		jobMap:         map[string]string{"remote-1": "local-1"},
		sessionCmdSeen: map[string]struct{}{},
	}
	cl.handleSessionCommand(context.Background(), wsproto.SessionCommand{
		JobID: "remote-1", CmdID: "say-img", Action: "say", Prompt: "see",
		Images: []wsproto.SessionImage{{Mime: "image/png", Data: []byte{1, 2}}, {Mime: "image/gif", Data: []byte{3}}},
	})
	if jobs.say != 0 || jobs.text != "see" || len(jobs.images) != 2 ||
		jobs.images[0].Mime != "image/png" || jobs.images[1].Mime != "image/gif" || string(jobs.images[0].Data) != "\x01\x02" {
		t.Fatalf("say=%d text=%q images=%+v", jobs.say, jobs.text, jobs.images)
	}
	cl.handleSessionCommand(context.Background(), wsproto.SessionCommand{JobID: "remote-1", CmdID: "say-txt", Action: "say", Prompt: "plain"})
	if jobs.say != 1 {
		t.Fatalf("text-only say = %d, want 1 plain say", jobs.say)
	}
}
