package stage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestMain drops the git environment of a surrounding checkout: the gates run git in
// temporary repositories and must never see someone else's GIT_DIR.
func TestMain(m *testing.M) {
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR"} {
		_ = os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

// fix is one test world: a store with a controllable clock, a temporary git repository
// registered as project "p", and a service wired to both.
type fix struct {
	t     *testing.T
	store *jobstore.Store
	svc   *Service
	repo  string
	cfg   *config.Config
	clock atomic.Int64
}

func newFix(t *testing.T) *fix {
	t.Helper()
	f := &fix{t: t, repo: t.TempDir()}
	f.clock.Store(1_000_000)
	s, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.SetClock(func() time.Time { return time.Unix(f.clock.Load(), 0) })
	f.store = s
	f.cfg = &config.Config{Projects: map[string]config.ProjectConfig{"p": {HostPath: f.repo}}}
	f.svc = New(Deps{Store: s, Config: func() *config.Config { return f.cfg }})
	f.git("init", "-q", "-b", "main")
	return f
}

// tick moves the clock forward so later writes are strictly after earlier ones.
func (f *fix) tick() { f.clock.Add(10) }

func (f *fix) git(args ...string) string {
	f.t.Helper()
	return gitIn(f.t, f.repo, args...)
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes a file and commits it, returning the new HEAD.
func (f *fix) commit(rel, content string) string {
	f.t.Helper()
	p := filepath.Join(f.repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "c "+rel)
	return f.git("rev-parse", "HEAD")
}

// item creates a work item of project p linked to issue "gofer-1" with a flow of template tpl.
func (f *fix) item(tpl string) string {
	f.t.Helper()
	v, err := f.svc.Start(StartInput{Issue: "gofer-1", Template: tpl, ProjectKey: "p", Title: "t", By: "human:a"})
	if err != nil {
		f.t.Fatal(err)
	}
	return v.WorkItemID
}

// stageAt moves the flow to the named step directly in the store (a gate test starts at the
// step it tests; the transition itself is tested through Done).
func (f *fix) stageAt(id, step string) {
	f.t.Helper()
	ws, ok, err := f.store.GetWorkStage(id)
	if err != nil || !ok {
		f.t.Fatalf("stage of %s: %v %v", id, ok, err)
	}
	f.tick()
	idx := -1
	for i, s := range ws.Steps {
		if s == step {
			idx = i
		}
	}
	if idx < 0 {
		f.t.Fatalf("step %s not in %v", step, ws.Steps)
	}
	applied, err := f.store.TransitionWorkStage(jobstore.StageTransition{WorkItemID: id, FromStep: ws.Step, ToStep: idx, ToStatus: jobstore.StageActive, Journal: "test move"})
	if err != nil || !applied {
		f.t.Fatalf("move to %s: %v %v", step, applied, err)
	}
	f.tick()
}

func (f *fix) gate(id string) Gate {
	f.t.Helper()
	v, err := f.svc.Status(id)
	if err != nil {
		f.t.Fatal(err)
	}
	if v.Gate == nil {
		f.t.Fatal("no gate in the view")
	}
	return *v.Gate
}

func (f *fix) link(id, kind, ref, role string) {
	f.t.Helper()
	if _, err := f.store.AddWorkLinkRole(id, kind, ref, role, "human:a"); err != nil {
		f.t.Fatal(err)
	}
}

// missingHas reports whether some Missing line contains every fragment.
func missingHas(g Gate, frags ...string) bool {
	for _, m := range g.Missing {
		ok := true
		for _, fr := range frags {
			if !strings.Contains(m, fr) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (f *fix) reviewJob(id, decision string) {
	f.t.Helper()
	if err := f.store.UpsertJob(jobstore.JobRecord{ID: id, ProjectKey: "p", Agent: "claude", Runner: "local", Status: "done"}); err != nil {
		f.t.Fatal(err)
	}
	if decision != "" {
		if _, err := f.store.SetJobSelfResult(id, decision, "r", "{}"); err != nil {
			f.t.Fatal(err)
		}
	}
}

func lines(n int, tail string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String() + tail
}
