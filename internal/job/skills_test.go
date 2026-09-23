package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// stubSkills is the job-side skill-library seam: it answers from the test's own
// fixtures, so a job's skills step is assertable without a skill store on disk.
type stubSkills struct {
	desc   map[string]string
	mount  int64
	staged []UploadSpec
}

func (l *stubSkills) Get(name string) (SkillInfo, bool) {
	d, ok := l.desc[name]
	if !ok {
		return SkillInfo{}, false
	}
	return SkillInfo{
		Name: name, Description: d, Size: 32,
		Files: []SkillFile{{Path: "SKILL.md", Size: 32}},
	}, true
}

func (l *stubSkills) Mount(name, dstDir string) (int64, error) {
	dir := filepath.Join(dstDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	body := "---\nname: " + name + "\ndescription: " + l.desc[name] + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		return 0, err
	}
	return int64(len(body)), nil
}

func (l *stubSkills) Stage(_ context.Context, name, runner, projectKey, caller string) ([]UploadSpec, error) {
	out := []UploadSpec{{XferID: "xf-" + name, Dest: skillDestFor(name, "SKILL.md"), Base: SkillBaseResultDir}}
	l.staged = append(l.staged, out...)
	return out, nil
}

// stubPeerRunner stands in for a configured peer-http runner. The skills policy only
// needs the runner's NAME to be classified (config type "peer-http"), so the test
// never opens a socket — it records the Forward the job service handed the peer.
type stubPeerRunner struct {
	got runner.Forward
}

func (r *stubPeerRunner) Name() string { return "peer-x" }

func (r *stubPeerRunner) Run(_ context.Context, req runner.Request) runner.Result {
	if req.Forward != nil {
		r.got = *req.Forward
	}
	return runner.Result{ExitCode: 0}
}

// newSkillService builds a Service over one cli-agent ("ok", which echoes its argv
// so the final prompt is readable in stdout) plus the built-in exec, with the named
// skills present in the stub library. Project "self" allows both.
func newSkillService(t *testing.T, root string, cfgMut func(*config.Config), names ...string) (*Service, *stubSkills) {
	t.Helper()
	return newSkillServiceRunners(t, root, cfgMut, nil, names...)
}

// newSkillServiceRunners is newSkillService with extra runner instances registered on
// the service, so a test can drive a remote-runner branch (a peer, a worker) without
// a socket: the classification reads the CONFIG type, the execution goes to the stub.
func newSkillServiceRunners(t *testing.T, root string, cfgMut func(*config.Config), extra map[string]runner.Runner, names ...string) (*Service, *stubSkills) {
	t.Helper()
	desc := map[string]string{}
	for _, n := range names {
		desc[n] = "desc of " + n
	}
	lib := &stubSkills{desc: desc}
	// The project's checkout is a SUBDIR of the test root: storage.root, the result
	// dirs and the metadata db all live in the root itself, so a "the working tree did
	// not change" assertion has one directory to watch.
	projDir := filepath.Join(root, "work")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: filepath.Join(root, "data")},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       projDir,
				AllowedAgents:  []string{"ok", agent.ExecAgentKey},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"ok": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"argv", "{{prompt}}"}},
		},
	}
	if cfgMut != nil {
		cfgMut(cfg)
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	for name, r := range extra {
		runners[name] = r
	}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	s := drainOnClose(t, NewService(cfg, projReg, agentReg, runners, meta, nil))
	s.SetSkillLibrary(lib)
	return s, lib
}

// promptOf reads the prompt the job's REQUEST carries (request_json) — the caller's
// own text, which is also what a rerun replays.
func promptOf(t *testing.T, final JobResult) string {
	t.Helper()
	var req JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("unmarshal request_json: %v", err)
	}
	return req.Prompt
}

// executedPromptOf reads the prompt the AGENT actually received from the child's own
// stdout (the test cli-agent echoes its argv). It is the only place the
// executing-machine skills list is observable: request_json keeps the caller's text.
func executedPromptOf(t *testing.T, final JobResult) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(final.ResultDir, "stdout.log"))
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	return strings.TrimRight(string(b), "\n")
}

// TestSkillsMountedToResultDirNotCwd: the bound skills land in the job's OWN result
// dir — never in the project working tree, which other jobs share and git watches.
func TestSkillsMountedToResultDirNotCwd(t *testing.T) {
	root := t.TempDir()
	s, _ := newSkillService(t, root, nil, "house-rules")

	projDir := filepath.Join(root, "work")
	before := listFiles(t, projDir)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "do the thing", TimeoutSec: 30,
		Skills: []string{"house-rules"},
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	got := filepath.Join(final.ResultDir, "skills", "house-rules", "SKILL.md")
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("mounted SKILL.md at %s: %v", got, err)
	}
	if after := listFiles(t, projDir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("the project working tree changed:\nbefore=%v\nafter=%v", before, after)
	}
}

// TestSkillPromptListsPaths (决策 1, 2026-09-23): the RUNNING prompt OPENS with the
// mounted-skills list (name + description + the SKILL.md path as the EXECUTING machine
// sees it) and the caller's own prompt follows untouched; the agent also gets
// GOFER_SKILLS_DIR. The list is rendered by the machine that mounted the files — the
// persisted request keeps the caller's text alone, so a rerun/audit replays the ask,
// not a path that belonged to one machine's result dir.
func TestSkillPromptListsPaths(t *testing.T) {
	root := t.TempDir()
	s, _ := newSkillService(t, root, nil, "house-rules")

	const body = "ORIGINAL-PROMPT-BODY"
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: body, TimeoutSec: 30,
		Skills: []string{"house-rules"},
	})
	prompt := executedPromptOf(t, final)
	if !strings.HasPrefix(prompt, skillsPromptHeader) {
		t.Fatalf("the executed prompt does not start with the skills list:\n%s", prompt)
	}
	wantPath := filepath.Join(final.ResultDir, skillsDirName, "house-rules", "SKILL.md")
	if !strings.Contains(prompt, wantPath) {
		t.Fatalf("the executed prompt is missing the executing machine's SKILL.md path %s:\n%s", wantPath, prompt)
	}
	if !strings.Contains(prompt, "desc of house-rules") {
		t.Fatalf("the executed prompt is missing the skill description:\n%s", prompt)
	}
	if !strings.HasSuffix(prompt, body) {
		t.Fatalf("the caller's prompt must follow the list untouched:\n%s", prompt)
	}
	if got := promptOf(t, final); got != body {
		t.Fatalf("request_json prompt = %q, want the caller's own text %q", got, body)
	}
	// The agent process can discover the mount without parsing the prompt.
	var cmd struct {
		EnvKeys []string `json:"env_keys"`
	}
	if err := json.Unmarshal([]byte(final.RenderedCommand), &cmd); err != nil {
		t.Fatalf("unmarshal rendered_command: %v", err)
	}
	if !hasString(cmd.EnvKeys, "GOFER_SKILLS_DIR") {
		t.Fatalf("env_keys = %v, want GOFER_SKILLS_DIR", cmd.EnvKeys)
	}
}

// TestPeerRunnerSkipsSkills (决策 2, 2026-09-23): a peer-http job mounts nothing and
// lists nothing — the peer's transport carries no files and this hub knows nothing
// about the peer's paths, so a path the peer cannot read must never be promised. The
// job still runs and its row keeps the binding it was decided with; the omission is
// the job.skills_skipped{peer_runner} event.
func TestPeerRunnerSkipsSkills(t *testing.T) {
	root := t.TempDir()
	peer := &stubPeerRunner{}
	s, lib := newSkillServiceRunners(t, root, func(c *config.Config) {
		c.Runners = map[string]config.RunnerConfig{
			"peer-x": {Type: "peer-http", BaseURL: "http://peer.invalid"},
		}
		p := c.Projects["self"]
		p.AllowedRunners = []string{"local", "peer-x"}
		c.Projects["self"] = p
	}, map[string]runner.Runner{"peer-x": peer}, "house-rules")

	const body = "PEER-PROMPT-BODY"
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "peer-x",
		Cwd: ".", Prompt: body, TimeoutSec: 30,
		Skills: []string{"house-rules"},
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	if len(final.Skills) != 1 || final.Skills[0] != "house-rules" {
		t.Fatalf("job skills = %v, want the decided binding on the row", final.Skills)
	}
	if peer.got.Prompt != body {
		t.Fatalf("the peer's prompt = %q, want the caller's text %q", peer.got.Prompt, body)
	}
	if len(peer.got.Skills) != 0 {
		t.Fatalf("peer forward skills = %v, want none", peer.got.Skills)
	}
	if len(peer.got.Uploads) != 0 || len(lib.staged) != 0 {
		t.Fatalf("a peer job staged skill files: forward=%+v staged=%+v", peer.got.Uploads, lib.staged)
	}
	if _, err := os.Stat(filepath.Join(final.ResultDir, skillsDirName)); err == nil {
		t.Fatal("a peer job mounted a skills dir")
	}
	events, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Type != EventJobSkillsSkipped {
			continue
		}
		found = true
		if !strings.Contains(e.Detail, "peer_runner") {
			t.Fatalf("job.skills_skipped detail = %s, want reason peer_runner", e.Detail)
		}
	}
	if !found {
		t.Fatalf("no %s event among %d events", EventJobSkillsSkipped, len(events))
	}
}

// TestNoSkillsDisablesAll: `--no-skills` mounts nothing, an exec agent never gets
// skills, and a name that is not in the library is a REJECTED submit that says which
// one — binding a typo must never run a job that quietly lacks its rules.
func TestNoSkillsDisablesAll(t *testing.T) {
	root := t.TempDir()

	t.Run("no-skills", func(t *testing.T) {
		s, _ := newSkillService(t, root, nil, "house-rules")
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
			Skills: []string{"house-rules"}, NoSkills: true,
		})
		if len(final.Skills) != 0 {
			t.Fatalf("skills = %v, want none", final.Skills)
		}
		if _, err := os.Stat(filepath.Join(final.ResultDir, "skills")); err == nil {
			t.Fatal("--no-skills still mounted a skills dir")
		}
		if strings.HasPrefix(executedPromptOf(t, final), skillsPromptHeader) {
			t.Fatal("--no-skills still rendered the skills list")
		}
	})

	t.Run("exec agent", func(t *testing.T) {
		s, _ := newSkillService(t, root, func(c *config.Config) {
			c.Server.Skills = []string{"house-rules"}
		}, "house-rules")
		final := submitAndWait(t, s, JobRequest{
			ProjectKey: "self", Agent: agent.ExecAgentKey, Runner: "local",
			Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		})
		if len(final.Skills) != 0 {
			t.Fatalf("exec job skills = %v, want none", final.Skills)
		}
		if _, err := os.Stat(filepath.Join(final.ResultDir, "skills")); err == nil {
			t.Fatal("an exec job mounted a skills dir")
		}
	})

	t.Run("unknown name", func(t *testing.T) {
		s, _ := newSkillService(t, root, nil, "house-rules")
		_, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
			Skills: []string{"nope"},
		})
		if err == nil {
			t.Fatal("Submit with an unknown skill succeeded, want a rejection")
		}
		if !strings.Contains(err.Error(), "nope") {
			t.Fatalf("error = %v, want the unknown skill named", err)
		}
	})
}

// TestCollectExcludesSkillsDir: skills are INPUT, not output — a collect glob that
// reaches into the result dir never brings the mounted skills back as artifacts.
func TestCollectExcludesSkillsDir(t *testing.T) {
	root := t.TempDir()
	s, _ := newSkillService(t, root, func(c *config.Config) {
		// Put the result base INSIDE the project checkout: that is the layout that
		// makes a `--collect` glob genuinely able to reach the mounted skills
		// (<cwd>/logs/<project>/<date>/<job>/skills/<name>/SKILL.md).
		c.Storage.Root = filepath.Join(root, "work", "logs")
	}, "house-rules")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
		Skills:  []string{"house-rules"},
		Collect: []string{"logs/self/*/*/skills/*/SKILL.md"},
	})
	if final.Xfer == nil {
		t.Fatal("the glob never reached the mount, so this test proved nothing")
	}
	var skipped int
	for _, c := range final.Xfer.Collected {
		if strings.Contains(c.Name, "/skills/") {
			t.Fatalf("collect brought a mounted skill back: %s", c.Name)
		}
	}
	for _, s := range final.Xfer.Skipped {
		if strings.Contains(s.Reason, "skills") {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatalf("the matched skill file was neither collected nor explained: %+v", final.Xfer)
	}
}

// TestSkillsMountedEvent: a successful mount is observable — the event carries the
// names and the bytes, and the job row says which skills it ran with.
func TestSkillsMountedEvent(t *testing.T) {
	root := t.TempDir()
	s, _ := newSkillService(t, root, nil, "house-rules")
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
		Skills: []string{"house-rules"},
	})
	if len(final.Skills) != 1 || final.Skills[0] != "house-rules" {
		t.Fatalf("job skills = %v, want [house-rules]", final.Skills)
	}
	events, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Type != EventJobSkillsMounted {
			continue
		}
		found = true
		if !strings.Contains(e.Detail, "house-rules") {
			t.Fatalf("job.skills_mounted detail = %s, want the mounted name", e.Detail)
		}
	}
	if !found {
		t.Fatalf("no %s event among %d events", EventJobSkillsMounted, len(events))
	}
}

// listFiles is the cwd-unchanged probe: every path under root, relative and sorted.
func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
