package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/store"
)

func TestSaySessionBlocksSendsImagesAfterText(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{ImageCapable: true, RecordBlocks: true})
	created := submitSmokeSession(t, s, 20)
	waitSessionTurn(t, s, created.ID, 1)

	ok, err := s.SessionAcceptsImages(created.ID)
	if err != nil || !ok {
		t.Fatalf("SessionAcceptsImages = %v, %v; want true", ok, err)
	}
	err = s.SaySessionBlocks(created.ID, "what is this?", []ImageInput{
		{Mime: "image/png", Data: []byte{1, 2, 3}}, {Mime: "image/jpeg", Data: []byte{9, 9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionTurn(t, s, created.ID, 2)

	// The capability is persisted with the session row.
	got, found := s.Get(created.ID)
	if !found || !got.SessionImages {
		t.Fatalf("persisted result lost SessionImages: found=%v %+v", found, got)
	}
	if rec, found, err := s.meta.GetJob(created.ID); err != nil || !found || !strings.Contains(rec.SessionStateJSON, `"session_images":true`) {
		t.Fatalf("session_state_json = %q found=%v err=%v", rec.SessionStateJSON, found, err)
	}

	if err := s.EndSession(created.ID); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Wait(created.ID)
	log := readJobLog(t, final, store.StderrFile)
	if !strings.Contains(log, "acptest: prompt-blocks text\n") || !strings.Contains(log, "acptest: prompt-blocks text|image/png:3|image/jpeg:2\n") {
		t.Fatalf("agent blocks:\n%s", log)
	}
}

func TestSaySessionBlocksNotSupported(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{RecordBlocks: true})
	created := submitSmokeSession(t, s, 20)
	waitSessionTurn(t, s, created.ID, 1)

	if ok, err := s.SessionAcceptsImages(created.ID); err != nil || ok {
		t.Fatalf("SessionAcceptsImages = %v, %v; want false", ok, err)
	}
	err := s.SaySessionBlocks(created.ID, "see", []ImageInput{{Mime: "image/png", Data: []byte{1}}})
	if !errors.Is(err, ErrImagesNotSupported) {
		t.Fatalf("err = %v, want ErrImagesNotSupported", err)
	}
	// Nothing was queued: a plain say still works as the next turn.
	if err := s.SaySession(created.ID, "plain"); err != nil {
		t.Fatalf("say after refused images: %v", err)
	}
	waitSessionTurn(t, s, created.ID, 2)
	if err := s.EndSession(created.ID); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Wait(created.ID)
	if log := readJobLog(t, final, store.StderrFile); strings.Contains(log, "image/") {
		t.Fatalf("an image block reached a non-image agent:\n%s", log)
	}
}

func TestSessionAcceptsImagesUnknownJob(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{})
	if _, err := s.SessionAcceptsImages("nope"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("err = %v, want ErrJobNotFound", err)
	}
}

// imageRemoteSender extends the remote sender with the v25 image say.
type imageRemoteSender struct {
	remoteSessionSender
	images [][]ImageInput
}

func (r *imageRemoteSender) SendSessionSay(_, _, _, prompt string, images []ImageInput) error {
	r.sent = append(r.sent, "say-images:"+prompt)
	r.images = append(r.images, images)
	return nil
}

func newRemoteImageSession(t *testing.T, sender WorkerSelector, images bool) (*Service, JobResult) {
	t.Helper()
	s := newWorkerTestServiceSel(t, t.TempDir(), &stubWorkerRunner{}, nil, sender)
	result := JobResult{
		ID: "remote-session-img", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", Session: true, Status: StatusAwaitingInput, TurnNo: 1,
		Cwd: ".", ResultDir: t.TempDir(), SessionImages: images,
	}
	if err := s.persist(result); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.jobs[result.ID] = &jobEntry{result: result, done: make(chan struct{})}
	s.mu.Unlock()
	return s, result
}

func TestRemoteSessionSaySessionBlocks(t *testing.T) {
	imgs := []ImageInput{{Mime: "image/png", Data: []byte{1}}}

	t.Run("worker reported image support", func(t *testing.T) {
		sender := &imageRemoteSender{}
		s, r := newRemoteImageSession(t, sender, true)
		if ok, err := s.SessionAcceptsImages(r.ID); err != nil || !ok {
			t.Fatalf("SessionAcceptsImages = %v, %v", ok, err)
		}
		if err := s.SaySessionBlocks(r.ID, "see", imgs); err != nil {
			t.Fatal(err)
		}
		if len(sender.images) != 1 || len(sender.images[0]) != 1 || sender.images[0][0].Mime != "image/png" || sender.sent[0] != "say-images:see" {
			t.Fatalf("sent=%v images=%+v", sender.sent, sender.images)
		}
	})

	t.Run("old worker never reported it", func(t *testing.T) {
		sender := &imageRemoteSender{}
		s, r := newRemoteImageSession(t, sender, false)
		if ok, err := s.SessionAcceptsImages(r.ID); err != nil || ok {
			t.Fatalf("SessionAcceptsImages = %v, %v; want false", ok, err)
		}
		if err := s.SaySessionBlocks(r.ID, "see", imgs); !errors.Is(err, ErrImagesNotSupported) {
			t.Fatalf("err = %v, want ErrImagesNotSupported", err)
		}
		if len(sender.sent) != 0 {
			t.Fatalf("something was sent: %v", sender.sent)
		}
	})

	t.Run("transport without image say", func(t *testing.T) {
		sender := &remoteSessionSender{}
		s, r := newRemoteImageSession(t, sender, true)
		if err := s.SaySessionBlocks(r.ID, "see", imgs); !errors.Is(err, ErrImagesNotSupported) {
			t.Fatalf("err = %v, want ErrImagesNotSupported", err)
		}
		if len(sender.sent) != 0 {
			t.Fatalf("something was sent: %v", sender.sent)
		}
	})

	t.Run("worker status report sets the capability", func(t *testing.T) {
		s, r := newRemoteImageSession(t, &imageRemoteSender{}, false)
		s.applyRemoteSessionState(s.entry(r.ID), WorkerInflightJob{JobID: r.ID, SessionStatus: StatusAwaitingInput, TurnNo: 1, SessionImages: true}, false)
		if ok, _ := s.SessionAcceptsImages(r.ID); !ok {
			t.Fatal("capability reported by the worker was not recorded")
		}
	})
}
