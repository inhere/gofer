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

// newSkillService builds a Service over one cli-agent ("ok", which echoes its argv
// so the final prompt is readable in stdout) plus the built-in exec, with the named
// skills present in the stub library. Project "self" allows both.
func newSkillService(t *testing.T, root string, cfgMut func(*config.Config), names ...string) (*Service, *stubSkills) {
	t.Helper()
	desc := map[string]string{}
	for _, n := range names {
		desc[n] = "desc of " + n
	}
	lib := &stubSkills{desc: desc}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
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
	s := newServiceFromCfg(t, root, cfg)
	s.SetSkillLibrary(lib)
	return s, lib
}

// promptOf reads the prompt the job actually ran with out of request_json (the same
// text the executing machine renders into argv).
func promptOf(t *testing.T, final JobResult) string {
	t.Helper()
	var req JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &req); err != nil {
		t.Fatalf("unmarshal request_json: %v", err)
	}
	return req.Prompt
}

// TestSkillsMountedToResultDirNotCwd: the bound skills land in the job's OWN result
// dir — never in the project working tree, which other jobs share and git watches.
func TestSkillsMountedToResultDirNotCwd(t *testing.T) {
	root := t.TempDir()
	s, _ := newSkillService(t, root, nil, "house-rules")

	before := listFiles(t, root)
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
	if after := listFiles(t, root); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("the project working tree changed:\nbefore=%v\nafter=%v", before, after)
	}
}

// TestSkillPromptListsPaths: the final prompt OPENS with the mounted-skills list
// (name + description + the SKILL.md path as the EXECUTING machine sees it) and the
// caller's own prompt follows untouched; the agent also gets GOFER_SKILLS_DIR.
func TestSkillPromptListsPaths(t *testing.T) {
	root := t.TempDir()
	s, _ := newSkillService(t, root, nil, "house-rules")

	const body = "ORIGINAL-PROMPT-BODY"
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: body, TimeoutSec: 30,
		Skills: []string{"house-rules"},
	})
	prompt := promptOf(t, final)
	if !strings.HasPrefix(prompt, skillsPromptHeader) {
		t.Fatalf("prompt does not start with the skills list:\n%s", prompt)
	}
	wantPath := filepath.Join(final.ResultDir, skillsDirName, "house-rules", "SKILL.md")
	if !strings.Contains(prompt, wantPath) {
		t.Fatalf("prompt is missing the executing machine's SKILL.md path %s:\n%s", wantPath, prompt)
	}
	if !strings.Contains(prompt, "desc of house-rules") {
		t.Fatalf("prompt is missing the skill description:\n%s", prompt)
	}
	if !strings.Contains(prompt, body) {
		t.Fatalf("the caller's prompt is gone:\n%s", prompt)
	}
	if strings.Index(prompt, body) < strings.Index(prompt, skillsPromptHeader) {
		t.Fatal("the skills list must come BEFORE the caller's prompt")
	}
	// The agent process can discover the mount without parsing the prompt.
	var cmd struct {
		EnvKeys []string `json:"env_keys"`
	}
	if err := json.Unmarshal([]byte(final.RenderedCommand), &cmd); err != nil {
		t.Fatalf("unmarshal rendered_command: %v", err)
	}
	if !contains(cmd.EnvKeys, "GOFER_SKILLS_DIR") {
		t.Fatalf("env_keys = %v, want GOFER_SKILLS_DIR", cmd.EnvKeys)
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
		if strings.HasPrefix(promptOf(t, final), skillsPromptHeader) {
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
		// Put the result base INSIDE the project root so a collect glob can reach it
		// (the default layout is <root>/<project>/<date>/<job>/).
		c.Storage.Root = filepath.Join(root, "logs")
	}, "house-rules")

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local", Cwd: ".", Prompt: "x", TimeoutSec: 30,
		Skills:  []string{"house-rules"},
		Collect: []string{"logs/*/*/*/skills/*/SKILL.md", "logs/*/*/*/request.json"},
	})
	if final.Xfer == nil {
		t.Fatal("no xfer summary recorded")
	}
	for _, c := range final.Xfer.Collected {
		if strings.Contains(c.Name, "/skills/") {
			t.Fatalf("collect brought a mounted skill back: %s", c.Name)
		}
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

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
