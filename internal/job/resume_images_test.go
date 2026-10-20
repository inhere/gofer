package job

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// argImages makes the "claude" agent of the fixture an image_input: arg agent (the shape
// the built-in suga template has) and returns its resolved definition.
func argImages(t *testing.T, s *Service) config.AgentConfig {
	t.Helper()
	ac, ok := s.agents.Get("claude")
	if !ok {
		t.Fatal("fixture agent claude missing")
	}
	ac.ImageInput = config.ImageInputArg
	ac.ImageArgs = []string{"--image", "{{path}}"}
	// cliCarrierRequest re-reads the effective input from the config, so the override has
	// to be visible there too (a resolved registry copy is not enough).
	s.config().Agents["claude"] = ac
	return ac
}

// The image paths of an arg delivery ride the continuation argv: one image_args expansion
// per image, each path still a single argv element (a path with spaces is not split).
func TestResumeCarrierAppendsImageArgs(t *testing.T) {
	s := newClaudeInjectService(t, t.TempDir())
	ac := argImages(t, s)
	src := JobResult{ID: "src", ProjectKey: "self", Agent: "claude", Runner: "local", SessionID: "sid-img"}

	got, err := s.cliCarrierRequest(JobRequest{ProjectKey: "self"}, src, ac, "claude", "look",
		[]string{`D:\work\img one.png`, "/tmp/b.png"}, false, false)
	if err != nil {
		t.Fatalf("cliCarrierRequest: %v", err)
	}
	want := []string{"echo", "--resume", "sid-img", "-p", "look", "--image", `D:\work\img one.png`, "--image", "/tmp/b.png"}
	if !equalArgs(got.Cmd, want) {
		t.Fatalf("resumed argv = %#v, want %#v", got.Cmd, want)
	}
	// The paths are persisted with the carrier request, so a worker that runs it sees them.
	blob, jerr := json.Marshal(got)
	if jerr != nil {
		t.Fatalf("marshal request: %v", jerr)
	}
	if argv := resumeArgv(t, string(blob)); !equalArgs(argv, want) {
		t.Fatalf("persisted argv = %#v, want %#v", argv, want)
	}
}

// Only a batch continuation has an image flag: an interactive (pty) continuation and a
// resident ACP session refuse the paths instead of dropping them.
func TestResumeCarrierRefusesImagesWithoutArgv(t *testing.T) {
	s := newClaudeInjectService(t, t.TempDir())
	ac := argImages(t, s)
	src := JobResult{ID: "src", ProjectKey: "self", Agent: "claude", Runner: "local", SessionID: "sid-img"}

	_, err := s.cliCarrierRequest(JobRequest{ProjectKey: "self"}, src, ac, "claude", "look", []string{"/a.png"}, true, false)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("interactive continuation with images: err = %v, want ErrInvalidRequest", err)
	}
}

// An agent that does not take images on its command line is refused, never silently
// stripped of the user's images.
func TestResumeCarrierRefusesImagesForNonArgAgent(t *testing.T) {
	s := newClaudeInjectService(t, t.TempDir())
	ac, ok := s.agents.Get("claude")
	if !ok {
		t.Fatal("fixture agent claude missing")
	}
	src := JobResult{ID: "src", ProjectKey: "self", Agent: "claude", Runner: "local", SessionID: "sid-img"}

	_, err := s.cliCarrierRequest(JobRequest{ProjectKey: "self"}, src, ac, "claude", "look", []string{"/a.png"}, false, false)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("non-arg agent with images: err = %v, want ErrInvalidRequest", err)
	}
}
