package job

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// stubRules is the job-side rule-library seam: fixed bodies plus the digest the real
// store computes over them, so the injected prompt, the row's sha256 and the size
// check are all assertable without a library on disk.
type stubRules struct{ bodies map[string]string }

func newStubRules(bodies map[string]string) *stubRules { return &stubRules{bodies: bodies} }

// ruleSHA is the sha256 of one rule's stored bytes, spelled the way rule.Store does
// (the digest of the file verbatim).
func ruleSHA(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (l *stubRules) Get(name string) (RuleInfo, bool) {
	b, ok := l.bodies[name]
	if !ok {
		return RuleInfo{}, false
	}
	return RuleInfo{Name: name, SHA256: ruleSHA(b), Size: int64(len(b))}, true
}

func (l *stubRules) Body(name string) (string, error) {
	b, ok := l.bodies[name]
	if !ok {
		return "", fmt.Errorf("no rule %q", name)
	}
	return b, nil
}

// newRuleService is the skills harness (one cli-agent that echoes its argv, project
// "self", a temp checkout) plus the rule library, so a rule test can drive the real
// Submit → inject → persist path. skillNames are the skills the (separate) skill
// library knows; the rule bodies are the rule library's.
func newRuleService(t *testing.T, root string, cfgMut func(*config.Config), rules map[string]string, skillNames ...string) (*Service, *stubRules) {
	t.Helper()
	s, _ := newSkillService(t, root, cfgMut, skillNames...)
	lib := newStubRules(rules)
	s.SetRuleLibrary(lib)
	return s, lib
}

// TestProjectRulesFileAutoIncluded: a checkout's own `.gofer/RULES.md` becomes the
// rule `project:<key>` AFTER the bound rules (design §一.2) — no registration, it
// travels with the repository — and a checkout without one contributes nothing (the
// common case, which must not add an empty section).
func TestProjectRulesFileAutoIncluded(t *testing.T) {
	root := t.TempDir()
	s, _ := newRuleService(t, root, nil, map[string]string{"house-rules": "HOUSE-RULE-TEXT"})

	projDir := filepath.Join(root, "work")
	repoRule := "---\ndescription: repo conventions\n---\n\nREPO-RULE-TEXT\n"
	if err := os.MkdirAll(filepath.Join(projDir, ".gofer"), 0o755); err != nil {
		t.Fatalf("mkdir .gofer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir, filepath.FromSlash(projectRulesFile)), []byte(repoRule), 0o644); err != nil {
		t.Fatalf("write RULES.md: %v", err)
	}

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "do the thing", TimeoutSec: 30,
		Rules: []string{"house-rules"},
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	prompt := promptOf(t, final)
	if !strings.Contains(prompt, "### house-rules") || !strings.Contains(prompt, "HOUSE-RULE-TEXT") {
		t.Fatalf("the injected section is missing the bound rule:\n%s", prompt)
	}
	if !strings.Contains(prompt, "### project:self") || !strings.Contains(prompt, "REPO-RULE-TEXT") {
		t.Fatalf("the project's .gofer/RULES.md was not auto-included:\n%s", prompt)
	}
	// The project rule comes AFTER the bound ones (broadest first) and its
	// frontmatter is metadata, never injected text.
	if strings.Index(prompt, "### house-rules") > strings.Index(prompt, "### project:self") {
		t.Fatalf("project rule must follow the bound rules:\n%s", prompt)
	}
	if strings.Contains(prompt, "repo conventions") {
		t.Fatalf("the rule frontmatter leaked into the prompt:\n%s", prompt)
	}
	// The row records it with the sha256 of the FILE (what the row can be compared
	// against later), so `job show` names the version that ran.
	var repoRef *RuleRef
	for i, ref := range final.Rules {
		if ref.Name == "project:self" {
			repoRef = &final.Rules[i]
		}
	}
	if repoRef == nil {
		t.Fatalf("row rules = %+v, want a project:self entry", final.Rules)
	}
	if repoRef.SHA256 != ruleSHA(repoRule) {
		t.Fatalf("project:self sha256 = %q, want the file digest %q", repoRef.SHA256, ruleSHA(repoRule))
	}

	// A checkout without the file adds no rule at all: no empty `### project:self`
	// header, and no `project:` entry on the row.
	if err := os.Remove(filepath.Join(projDir, filepath.FromSlash(projectRulesFile))); err != nil {
		t.Fatalf("remove RULES.md: %v", err)
	}
	bare := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "do the thing", TimeoutSec: 30,
		Rules: []string{"house-rules"},
	})
	if bare.Status != StatusDone {
		t.Fatalf("second job status = %s (err=%s)", bare.Status, bare.Error)
	}
	if p := promptOf(t, bare); strings.Contains(p, "project:self") {
		t.Fatalf("a checkout with no .gofer/RULES.md must add no rule:\n%s", p)
	}
	for _, ref := range bare.Rules {
		if strings.HasPrefix(ref.Name, ProjectRulePrefix) {
			t.Fatalf("row rules = %+v, want no project entry", bare.Rules)
		}
	}
}

// TestRulesInjectedAtPromptTop: the final prompt the AGENT receives opens with the
// MANDATORY rules section, then the skills list, then the caller's own text (rules
// are the frame, the skills list is content). The section is part of request_json
// (that is the auditable record of what the agent was told to obey), unlike the
// skills list, which the executing machine renders from its own paths.
func TestRulesInjectedAtPromptTop(t *testing.T) {
	root := t.TempDir()
	s, _ := newRuleService(t, root, nil, map[string]string{"house-rules": "NEVER push."}, "house-rules")

	const body = "ORIGINAL-PROMPT-BODY"
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: body, TimeoutSec: 30,
		Rules:  []string{"house-rules"},
		Skills: []string{"house-rules"},
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	prompt := executedPromptOf(t, final)
	if !strings.HasPrefix(prompt, rulesPromptHeader) {
		t.Fatalf("the executed prompt does not start with the rules section:\n%s", prompt)
	}
	rulesAt := strings.Index(prompt, rulesPromptHeader)
	skillsAt := strings.Index(prompt, skillsPromptHeader)
	bodyAt := strings.Index(prompt, body)
	if skillsAt < 0 {
		t.Fatalf("the skills list is missing from the executed prompt:\n%s", prompt)
	}
	if !(rulesAt < skillsAt && skillsAt < bodyAt) {
		t.Fatalf("prompt order must be rules → skills → body (rules@%d skills@%d body@%d):\n%s",
			rulesAt, skillsAt, bodyAt, prompt)
	}
	if !strings.Contains(prompt, "### house-rules") || !strings.Contains(prompt, "NEVER push.") {
		t.Fatalf("the rules section is missing the rule's text:\n%s", prompt)
	}
	if !strings.HasSuffix(prompt, body) {
		t.Fatalf("the caller's prompt must follow the sections untouched:\n%s", prompt)
	}
	// request_json carries the section (the audit trail) but NOT the skills list,
	// which belongs to the executing machine's result dir.
	stored := promptOf(t, final)
	if !strings.HasPrefix(stored, rulesPromptHeader) || !strings.Contains(stored, "NEVER push.") {
		t.Fatalf("request_json prompt must carry the injected rules:\n%s", stored)
	}
	if strings.Contains(stored, skillsPromptHeader) {
		t.Fatalf("request_json must not carry the machine-rendered skills list:\n%s", stored)
	}
}

// TestRulesSizeLimitRejects: rules are short and hard, so a set over
// server.rules_max_bytes is a rejected submit that names the biggest offenders with
// their byte counts — the operator needs to know WHICH rule to shorten.
func TestRulesSizeLimitRejects(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 200)
	small := "tiny"
	s, _ := newRuleService(t, root, func(c *config.Config) {
		c.Server.RulesMaxBytes = 64
	}, map[string]string{"house-rules": big, "gofer-repo": small})

	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "x", TimeoutSec: 30,
		Rules: []string{"house-rules", "gofer-repo"},
	})
	if err == nil {
		t.Fatal("a submit over server.rules_max_bytes must be rejected")
	}
	msg := err.Error()
	for _, want := range []string{"rules_max_bytes", "64", "house-rules", "200 bytes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("rejection %q must mention %q", msg, want)
		}
	}

	// The same rules under the default 16KiB cap are admitted: the limit is the
	// configured number, not a hard-coded one.
	s2, _ := newRuleService(t, t.TempDir(), nil, map[string]string{"house-rules": big, "gofer-repo": small})
	final := submitAndWait(t, s2, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "x", TimeoutSec: 30,
		Rules: []string{"house-rules"},
	})
	if final.Status != StatusDone {
		t.Fatalf("under the default cap the job must run, status = %s (err=%s)", final.Status, final.Error)
	}
}

// TestRulesRecordedWithSha: the job ROW records each injected rule with the sha256 of
// the text that went into its prompt (jobs.rules_json), and the timeline carries the
// job.rules_injected receipt — the pair that answers "which discipline, which version"
// after the library has moved on.
func TestRulesRecordedWithSha(t *testing.T) {
	root := t.TempDir()
	body := "NEVER push."
	s, _ := newRuleService(t, root, nil, map[string]string{"house-rules": body})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "x", TimeoutSec: 30,
		Rules: []string{"house-rules"},
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	if len(final.Rules) != 1 || final.Rules[0].Name != "house-rules" || final.Rules[0].SHA256 != ruleSHA(body) {
		t.Fatalf("result rules = %+v, want house-rules@%s", final.Rules, ruleSHA(body))
	}

	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s): ok=%v err=%v", final.ID, ok, err)
	}
	row := fromRecord(rec)
	if len(row.Rules) != 1 || row.Rules[0].Name != "house-rules" || row.Rules[0].SHA256 != ruleSHA(body) {
		t.Fatalf("row rules = %+v, want house-rules@%s", row.Rules, ruleSHA(body))
	}
	// An old/empty row must not read back as one that injected rules.
	if got := fromRecord(jobstore.JobRecord{}).Rules; len(got) != 0 {
		t.Fatalf("empty row rules = %+v, want none", got)
	}

	events, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var found bool
	for _, e := range events {
		if e.Type != EventJobRulesInjected {
			continue
		}
		found = true
		if !strings.Contains(string(e.Detail), "house-rules") {
			t.Fatalf("job.rules_injected detail = %s, want the rule names", e.Detail)
		}
		if !strings.Contains(string(e.Detail), fmt.Sprintf("%d", len(body))) {
			t.Fatalf("job.rules_injected detail = %s, want the byte count %d", e.Detail, len(body))
		}
	}
	if !found {
		t.Fatalf("no %s event on the job timeline (%d events)", EventJobRulesInjected, len(events))
	}
}

// TestResumeDoesNotReinjectRules: a continuation is NOT given the rules again — the
// session it continues already carries them (design §一.3) — so neither the resumed
// request nor its argv/template contains the section.
func TestResumeDoesNotReinjectRules(t *testing.T) {
	root := t.TempDir()
	s := newRuleResumeService(t, root, map[string]string{"house-rules": "NEVER push."})

	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Prompt: "remember 42", Cwd: ".", TimeoutSec: 30,
	})
	if !strings.Contains(promptOf(t, src), rulesPromptHeader) {
		t.Fatalf("setup: the source job carries no injected rules:\n%s", promptOf(t, src))
	}
	if src.SessionID == "" {
		t.Fatal("setup: the claude source has no session id to resume")
	}

	resumed, err := s.ResumeJob(src.ID, "what number", "", "caller-7")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(resumed.ID); s.Wait(resumed.ID) })
	if strings.Contains(resumed.RequestJSON, rulesPromptHeader) ||
		strings.Contains(resumed.RequestJSON, rulesPromptEnd) {
		t.Fatalf("the resumed request must not be re-injected with rules:\n%s", resumed.RequestJSON)
	}
}

// TestRerunReresolvesRules: a rerun re-reads the CURRENT rules — the version recorded
// on the source row is not replayed — so editing a rule changes what the next run is
// told to obey (design §一.3).
func TestRerunReresolvesRules(t *testing.T) {
	root := t.TempDir()
	lib := map[string]string{"house-rules": "FIRST-VERSION"}
	s, _ := newRuleService(t, root, nil, lib)

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "x", TimeoutSec: 30, Rules: []string{"house-rules"},
	})
	if src.Status != StatusDone {
		t.Fatalf("source status = %s (err=%s)", src.Status, src.Error)
	}
	if !strings.Contains(promptOf(t, src), "FIRST-VERSION") {
		t.Fatalf("setup: source prompt:\n%s", promptOf(t, src))
	}

	lib["house-rules"] = "SECOND-VERSION"
	rerun, err := s.RebuildJob(src.ID, RebuildOverrides{}, "caller-1", "127.0.0.1")
	if err != nil {
		t.Fatalf("RebuildJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(rerun.ID); s.Wait(rerun.ID) })
	rec, ok, err := s.meta.GetJob(rerun.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob(%s): ok=%v err=%v", rerun.ID, ok, err)
	}
	row := fromRecord(rec)
	if strings.Contains(row.RequestJSON, "FIRST-VERSION") {
		t.Fatalf("the rerun replayed the OLD rules text:\n%s", row.RequestJSON)
	}
	if !strings.Contains(row.RequestJSON, "SECOND-VERSION") {
		t.Fatalf("the rerun did not re-read the rule:\n%s", row.RequestJSON)
	}
	if len(row.Rules) != 1 || row.Rules[0].SHA256 != ruleSHA("SECOND-VERSION") {
		t.Fatalf("rerun row rules = %+v, want house-rules@%s", row.Rules, ruleSHA("SECOND-VERSION"))
	}
}

// newRuleResumeService is the resume harness (newResumeRunnableService, resume_test.go)
// plus a rule library and a deployment-wide binding, so a continuation's injected (or
// deliberately NOT injected) rules are assertable.
func newRuleResumeService(t *testing.T, root string, rules map[string]string) *Service {
	t.Helper()
	cfg := &config.Config{
		Server:  config.ServerConfig{Rules: []string{"house-rules"}},
		Storage: config.StorageConfig{Root: filepath.Join(root, "data")},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"claude", agent.ExecAgentKey},
				AllowedRunners: []string{"local"},
				AllowExec:      true, // resume submits an exec job
			},
		},
		Agents: map[string]config.AgentConfig{
			// The command stays literal ("claude") so the resumed argv keeps its real
			// shape; the source job is cancelled before it could ever execute.
			"claude": {Type: agent.TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	s.SetRuleLibrary(newStubRules(rules))
	return s
}
