package job

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

func TestParseKnowledge(t *testing.T) {
	report := "## 结果\nok\n\n## 发现但不碰\n- a.go：坏味道\n\n## 可复用经验\n\n- 改 schema 先跑迁移测试\n- Go 与 TS 两份解析器要同步\n  （共用一组用例）\n\n## 验收\n- 满足\n"
	want := []string{"改 schema 先跑迁移测试", "Go 与 TS 两份解析器要同步 （共用一组用例）"}
	if got := ParseKnowledge(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseKnowledge = %q, want %q", got, want)
	}
	// The findings section of the same report is unaffected by the generalisation.
	if got := ParseFindings(report); !reflect.DeepEqual(got, []string{"a.go：坏味道"}) {
		t.Fatalf("ParseFindings = %q", got)
	}
	// The 「交付约定」 line names the section inline — that is not a heading.
	if got := ParseKnowledge(knowledgeSectionLine + "\n- not an item\n"); got != nil {
		t.Fatalf("inline mention parsed: %q", got)
	}
	if got := ParseReportSection("### **可复用经验**：\n- x\n", KnowledgeSectionTitle, "Reusable"); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("decorated title: %q", got)
	}
}

// knowledgeService is a Service with one echo cli-agent: the job's report IS its
// prompt, so a prompt carrying a 「## 可复用经验」 section is a delivery that reports it.
func knowledgeService(t *testing.T, mode string) (*Service, string) {
	return knowledgeServiceIn(t, "", mode)
}

// knowledgeServiceIn is knowledgeService with an explicit project workspace root ("" =
// a fresh TempDir).
func knowledgeServiceIn(t *testing.T, root, mode string) (*Service, string) {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"echo"}, AllowedRunners: []string{"local"}, KnowledgeCapture: mode},
		},
		Agents: map[string]config.AgentConfig{"echo": {Type: agent.TypeCLIAgent, Command: "echo", Args: []string{"{{prompt}}"}}},
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	return drainOnClose(t, NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg), runners, meta, nil)), root
}

const knowledgeReport = "done\n\n## 可复用经验\n- lesson one\n- lesson two\n"

func TestKnowledgeCaptureOnFinish(t *testing.T) {
	t.Run("on: the delivery's items become pending candidates, once", func(t *testing.T) {
		s, _ := knowledgeService(t, "on")
		final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "echo", Runner: "local", Prompt: knowledgeReport, Cwd: ".", TimeoutSec: 30})
		if final.Status != StatusDone || !final.KnowledgeCapture {
			t.Fatalf("status=%s capture=%v err=%s", final.Status, final.KnowledgeCapture, final.Error)
		}
		list := func() []jobstore.MemoryCandidate {
			rows, err := s.ListMemoryCandidates(jobstore.MemoryCandidateFilter{JobID: final.ID})
			if err != nil {
				t.Fatal(err)
			}
			return rows
		}
		rows := list()
		if len(rows) != 2 || rows[0].Text != "lesson one" || rows[1].Text != "lesson two" || rows[0].ProjectKey != "self" {
			t.Fatalf("candidates = %+v", rows)
		}
		s.captureKnowledge(final) // a second terminal pass (adoption / review) adds nothing
		if again := list(); len(again) != 2 {
			t.Fatalf("not idempotent: %+v", again)
		}
	})
	t.Run("off: no section, no candidates", func(t *testing.T) {
		s, _ := knowledgeService(t, "off")
		final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "echo", Runner: "local", Prompt: knowledgeReport, Cwd: ".", TimeoutSec: 30, TodoID: ""})
		if final.KnowledgeCapture {
			t.Fatalf("captured with knowledge_capture off")
		}
		rows, err := s.ListMemoryCandidates(jobstore.MemoryCandidateFilter{Status: "all"})
		if err != nil || len(rows) != 0 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
	})
}

func TestAcceptRejectMemoryCandidate(t *testing.T) {
	s, _ := knowledgeService(t, "on")
	if _, err := s.meta.AddMemoryCandidates("job-1", "self", []string{"lesson one", "lesson two", "lesson three"}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListMemoryCandidates(jobstore.MemoryCandidateFilter{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	one, two, three := rows[0].ID, rows[1].ID, rows[2].ID

	out, err := s.AcceptMemoryCandidate(one, AcceptMemoryCandidateInput{Key: "k1"}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if out.Candidate.Status != jobstore.MemoryCandidateAccepted || out.Candidate.MemoryKey != "k1" || out.Candidate.DecidedBy != "alice" {
		t.Fatalf("candidate = %+v", out.Candidate)
	}
	mem, err := s.meta.GetScopedMemory(jobstore.ScopedMemoryProject, "self", "k1")
	if err != nil || mem.Content != "lesson one" || mem.Kind != "note" || mem.Source != "job:job-1" {
		t.Fatalf("memory = %+v err=%v", mem, err)
	}
	if _, err := s.AcceptMemoryCandidate(one, AcceptMemoryCandidateInput{Key: "k9"}, "alice"); !errors.Is(err, jobstore.ErrMemoryCandidateDecided) {
		t.Fatalf("second accept: %v", err)
	}
	// An existing key is refused and the candidate stays pending.
	if _, err := s.AcceptMemoryCandidate(two, AcceptMemoryCandidateInput{Key: "k1"}, "alice"); !errors.Is(err, ErrMemoryCandidateKeyExists) {
		t.Fatalf("existing key: %v", err)
	}
	if c, _ := s.meta.GetMemoryCandidate(two); c.Status != jobstore.MemoryCandidatePending {
		t.Fatalf("candidate moved on a refused accept: %+v", c)
	}
	for name, in := range map[string]AcceptMemoryCandidateInput{
		"no key":     {},
		"bad kind":   {Key: "k2", Kind: "handoff"},
		"two scopes": {Key: "k2", Global: true, Project: "self"},
		"too long":   {Key: "k2", Summary: strings.Repeat("长", 81)},
	} {
		if _, err := s.AcceptMemoryCandidate(two, in, "alice"); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Global scope, rule kind.
	if _, err := s.AcceptMemoryCandidate(two, AcceptMemoryCandidateInput{Key: "k2", Kind: "rule", Global: true}, "alice"); err != nil {
		t.Fatal(err)
	}
	if mem, err := s.meta.GetScopedMemory(jobstore.ScopedMemoryGlobal, "", "k2"); err != nil || mem.Kind != "rule" {
		t.Fatalf("global memory = %+v err=%v", mem, err)
	}
	// Reject only changes the state.
	cand, err := s.RejectMemoryCandidate(three, "bob")
	if err != nil || cand.Status != jobstore.MemoryCandidateRejected || cand.DecidedBy != "bob" {
		t.Fatalf("reject = %+v err=%v", cand, err)
	}
	if _, err := s.RejectMemoryCandidate(three, "bob"); !errors.Is(err, jobstore.ErrMemoryCandidateDecided) {
		t.Fatalf("second reject: %v", err)
	}
	if _, err := s.RejectMemoryCandidate(9999, "bob"); !errors.Is(err, jobstore.ErrMemoryCandidateNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if pending, _ := s.ListMemoryCandidates(jobstore.MemoryCandidateFilter{}); len(pending) != 0 {
		t.Fatalf("pending left: %+v", pending)
	}
	if all, _ := s.ListMemoryCandidates(jobstore.MemoryCandidateFilter{Status: "all", ProjectKey: "self"}); len(all) != 3 {
		t.Fatalf("all: %+v", all)
	}
}

// TestKnowledgeCaptureScopesCandidates: a job running inside a nested checkout of a
// shared workspace tracker records its candidates with that checkout as their scope, so
// the lesson does not reach the jobs of the other checkouts (gofer-opv5). A job at the
// root of the tracker's own repository stays workspace-wide.
func TestKnowledgeCaptureScopesCandidates(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, ".gofer", "tracker"),  // the workspace tracker
		filepath.Join(root, "deploy-hub", ".git"), // a checkout of its own
		filepath.Join(root, "deploy-hub", "src"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := knowledgeServiceIn(t, root, "on")
	scopeOf := func(cwd string) []string {
		t.Helper()
		final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "echo", Runner: "local", Prompt: knowledgeReport, Cwd: cwd, TimeoutSec: 30})
		rows, err := s.ListMemoryCandidates(jobstore.MemoryCandidateFilter{JobID: final.ID})
		if err != nil || len(rows) != 2 {
			t.Fatalf("cwd %s: rows=%+v err=%v", cwd, rows, err)
		}
		return rows[0].WhenPaths
	}
	if got := scopeOf("deploy-hub/src"); len(got) != 1 || got[0] != "deploy-hub/**" {
		t.Fatalf("nested checkout scope = %v", got)
	}
	if got := scopeOf("."); len(got) != 0 {
		t.Fatalf("workspace root scope = %v", got)
	}
}

// TestAcceptMemoryCandidateInheritsScope: the accepted memory carries the candidate's
// scope, except in the global scope, where memories belong to no repository.
func TestAcceptMemoryCandidateInheritsScope(t *testing.T) {
	s, _ := knowledgeService(t, "on")
	add := func(text string) int64 {
		t.Helper()
		if _, err := s.meta.AddMemoryCandidates("job-scope", "self", []string{text}, []string{"deploy-hub/**"}); err != nil {
			t.Fatal(err)
		}
		rows, err := s.ListMemoryCandidates(jobstore.MemoryCandidateFilter{JobID: "job-scope"})
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		return rows[0].ID
	}
	out, err := s.AcceptMemoryCandidate(add("lesson one"), AcceptMemoryCandidateInput{Key: "scoped-1"}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if out.Memory.When == nil || len(out.Memory.When.Paths) != 1 || out.Memory.When.Paths[0] != "deploy-hub/**" {
		t.Fatalf("project memory when = %+v", out.Memory.When)
	}
	out, err = s.AcceptMemoryCandidate(add("lesson two"), AcceptMemoryCandidateInput{Key: "scoped-2", Global: true}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if out.Memory.When != nil {
		t.Fatalf("global memory when = %+v", out.Memory.When)
	}
}
