package agent

import (
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestRenderSessionID verifies the {{session_id}} placeholder substitutes (and
// stays one argv element).
func TestRenderSessionID(t *testing.T) {
	got := Render([]string{"--session-id", "{{session_id}}"}, Vars{SessionID: "u-123"})
	if len(got) != 2 || got[0] != "--session-id" || got[1] != "u-123" {
		t.Fatalf("render = %#v, want [--session-id u-123]", got)
	}
}

// TestRenderSystemPrompt verifies the {{system_prompt}} placeholder substitutes
// and stays one argv element (multi-word prompt is not re-tokenised — SR403).
func TestRenderSystemPrompt(t *testing.T) {
	got := Render([]string{"--append-system-prompt", "{{system_prompt}}"}, Vars{SystemPrompt: "You are a strict reviewer"})
	if len(got) != 2 || got[0] != "--append-system-prompt" || got[1] != "You are a strict reviewer" {
		t.Fatalf("render = %#v, want [--append-system-prompt 'You are a strict reviewer']", got)
	}
}

// TestBuiltinSystemInjectClaude: a declared agent with no system_inject gets the
// built-in template for its name — claude --append-system-prompt, codex
// -c developer_instructions= (实测定稿 2026-06-29, see registry.go).
func TestBuiltinSystemInjectClaude(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {Type: TypeCLIAgent, Command: "claude"},
		"codex":  {Type: TypeCLIAgent, Command: "codex"},
	}}
	claude, _ := ResolveAgent(cfg, "claude")
	if len(claude.SystemInject) != 2 || claude.SystemInject[0] != "--append-system-prompt" || claude.SystemInject[1] != "{{system_prompt}}" {
		t.Errorf("claude SystemInject = %#v, want [--append-system-prompt {{system_prompt}}]", claude.SystemInject)
	}
	codex, _ := ResolveAgent(cfg, "codex")
	if len(codex.SystemInject) != 2 || codex.SystemInject[0] != "-c" || codex.SystemInject[1] != "developer_instructions={{system_prompt}}" {
		t.Errorf("codex SystemInject = %#v, want [-c developer_instructions={{system_prompt}}]", codex.SystemInject)
	}
}

// TestExplicitSystemInjectWins: an explicit system_inject is not overwritten.
func TestExplicitSystemInjectWins(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {Type: TypeCLIAgent, Command: "claude", SystemInject: []string{"--sys", "{{system_prompt}}"}},
	}}
	ac, _ := ResolveAgent(cfg, "claude")
	if len(ac.SystemInject) != 2 || ac.SystemInject[0] != "--sys" {
		t.Errorf("explicit SystemInject overwritten: %#v", ac.SystemInject)
	}
}

// TestBuiltinSessionDefaultsClaude: a declared claude agent with no session
// fields gets the built-in inject + resume defaults, plus the TUI exit-banner
// capture regex that backs the inject up (F6).
func TestBuiltinSessionDefaultsClaude(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {Type: TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}},
	}}
	ac, ok := ResolveAgent(cfg, "claude")
	if !ok {
		t.Fatal("claude should resolve")
	}
	if len(ac.SessionInject) != 2 || ac.SessionInject[0] != "--session-id" || ac.SessionInject[1] != "{{session_id}}" {
		t.Errorf("SessionInject = %#v, want [--session-id {{session_id}}]", ac.SessionInject)
	}
	if ac.SessionCapture != builtinSessionDefaults["claude"].SessionCapture {
		t.Errorf("claude SessionCapture = %q, want the built-in exit-banner regex", ac.SessionCapture)
	}
	if len(ac.SessionResume) != 4 || ac.SessionResume[0] != "--resume" {
		t.Errorf("SessionResume = %#v, want claude resume template", ac.SessionResume)
	}
}

// TestBuiltinSessionDefaultsCodex: a declared codex agent gets the built-in
// capture regex + resume template; inject stays empty.
func TestBuiltinSessionDefaultsCodex(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"codex": {Type: TypeCLIAgent, Command: "codex", Args: []string{"exec", "{{prompt}}"}},
	}}
	ac, ok := ResolveAgent(cfg, "codex")
	if !ok {
		t.Fatal("codex should resolve")
	}
	if len(ac.SessionInject) != 0 {
		t.Errorf("codex SessionInject = %#v, want empty (codex uses capture)", ac.SessionInject)
	}
	if ac.SessionCapture != builtinSessionDefaults["codex"].SessionCapture {
		t.Errorf("codex SessionCapture = %q, want the built-in regex", ac.SessionCapture)
	}
	if len(ac.SessionResume) != 4 || ac.SessionResume[0] != "exec" || ac.SessionResume[1] != "resume" {
		t.Errorf("SessionResume = %#v, want codex resume template", ac.SessionResume)
	}
}

func TestInteractiveAliasSessionDefaultsFromCommand(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"tty-claude": {Type: TypeCLIAgent, Command: "claude", Interactive: true},
		"tty-codex":  {Type: TypeCLIAgent, Command: `C:\tools\codex.exe`, Interactive: true},
	}}
	claude, _ := ResolveAgent(cfg, "tty-claude")
	if len(claude.SessionInject) != 2 || claude.SessionInject[0] != "--session-id" {
		t.Fatalf("tty-claude SessionInject = %#v, want claude inject default", claude.SessionInject)
	}
	if len(claude.SessionResume) != 4 || claude.SessionResume[0] != "--resume" {
		t.Fatalf("tty-claude SessionResume = %#v, want claude resume default", claude.SessionResume)
	}
	if len(claude.SessionResumeInteractive) != 2 || claude.SessionResumeInteractive[0] != "--resume" {
		t.Fatalf("tty-claude SessionResumeInteractive = %#v, want claude interactive resume default", claude.SessionResumeInteractive)
	}

	codex, _ := ResolveAgent(cfg, "tty-codex")
	if codex.SessionCapture != builtinSessionDefaults["codex"].SessionCapture {
		t.Fatalf("tty-codex SessionCapture = %q, want codex capture default", codex.SessionCapture)
	}
	if len(codex.SessionResume) != 4 || codex.SessionResume[0] != "exec" {
		t.Fatalf("tty-codex SessionResume = %#v, want codex resume default", codex.SessionResume)
	}
	if len(codex.SessionResumeInteractive) != 2 || codex.SessionResumeInteractive[0] != "resume" {
		t.Fatalf("tty-codex SessionResumeInteractive = %#v, want codex interactive resume default", codex.SessionResumeInteractive)
	}
}

// TestNonInteractiveAliasDoesNotGainBuiltinSessionDefaults: the built-in table is
// consulted by COMMAND base name only for an INTERACTIVE agent, so a non-interactive
// `claude-sup` never silently inherits claude's inject (`--session-id`, which this
// alias's argv shape was never checked against). AGT-04 still hands it the generic
// fallback, which injects nothing.
func TestNonInteractiveAliasDoesNotGainBuiltinSessionDefaults(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude-sup": {Type: TypeCLIAgent, Command: "claude"},
	}}
	ac, _ := ResolveAgent(cfg, "claude-sup")
	if len(ac.SessionInject) != 0 {
		t.Fatalf("non-interactive claude alias gained claude's inject: %#v", ac.SessionInject)
	}
	if !IsFallbackCapture(ac.SessionCapture) {
		t.Fatalf("SessionCapture = %q, want the generic fallback", ac.SessionCapture)
	}
}

func TestBuiltinSessionResumeInteractiveDefaults(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {Type: TypeCLIAgent, Command: "claude"},
		"codex":  {Type: TypeCLIAgent, Command: "codex"},
	}}
	claude, _ := ResolveAgent(cfg, "claude")
	wantClaude := []string{"--resume", "{{session_id}}"}
	if !equalStringSlices(claude.SessionResumeInteractive, wantClaude) {
		t.Fatalf("claude SessionResumeInteractive = %#v, want %#v", claude.SessionResumeInteractive, wantClaude)
	}
	for _, arg := range claude.SessionResumeInteractive {
		if arg == "-p" {
			t.Fatalf("claude interactive resume template must not include -p: %#v", claude.SessionResumeInteractive)
		}
	}

	codex, _ := ResolveAgent(cfg, "codex")
	wantCodex := []string{"resume", "{{session_id}}"}
	if !equalStringSlices(codex.SessionResumeInteractive, wantCodex) {
		t.Fatalf("codex SessionResumeInteractive = %#v, want %#v", codex.SessionResumeInteractive, wantCodex)
	}
}

// TestExplicitSessionConfigWinsOverBuiltin: an explicit session field is NOT
// overwritten by the built-in default (per-field, independently).
func TestExplicitSessionConfigWinsOverBuiltin(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {
			Type:                     TypeCLIAgent,
			Command:                  "claude",
			SessionInject:            []string{"--sid", "{{session_id}}"},
			SessionCapture:           `custom:\s*(\S+)`,
			SessionResumeInteractive: []string{"resume-tui", "{{session_id}}"},
		},
	}}
	ac, _ := ResolveAgent(cfg, "claude")
	if len(ac.SessionInject) != 2 || ac.SessionInject[0] != "--sid" {
		t.Errorf("explicit SessionInject overwritten: %#v", ac.SessionInject)
	}
	if ac.SessionCapture != `custom:\s*(\S+)` {
		t.Errorf("explicit SessionCapture overwritten: %q", ac.SessionCapture)
	}
	// SessionResume was unset -> filled from built-in.
	if len(ac.SessionResume) != 4 || ac.SessionResume[0] != "--resume" {
		t.Errorf("SessionResume should default-fill: %#v", ac.SessionResume)
	}
	if !equalStringSlices(ac.SessionResumeInteractive, []string{"resume-tui", "{{session_id}}"}) {
		t.Errorf("explicit SessionResumeInteractive overwritten: %#v", ac.SessionResumeInteractive)
	}
}

func TestExplicitSessionResumeInteractiveWins(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {
			Type:                     TypeCLIAgent,
			Command:                  "claude",
			SessionResumeInteractive: []string{"custom-resume", "{{session_id}}"},
		},
	}}
	ac, _ := ResolveAgent(cfg, "claude")
	want := []string{"custom-resume", "{{session_id}}"}
	if !equalStringSlices(ac.SessionResumeInteractive, want) {
		t.Fatalf("SessionResumeInteractive = %#v, want explicit %#v", ac.SessionResumeInteractive, want)
	}
}

// TestFallbackSessionDefaultsForUnknownAgent (was TestNonSessionAgentUnchanged): a
// cli-agent the built-in table does not know is no longer left with empty session
// fields — AGT-04 fills the generic capture + resume templates — but gofer still
// injects nothing, because it cannot invent an id for a CLI whose `--session-id`
// semantics it has never seen (an injected unknown flag would break the argv).
func TestFallbackSessionDefaultsForUnknownAgent(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"other": {Type: TypeCLIAgent, Command: "other", Args: []string{"{{prompt}}"}},
	}}
	ac, _ := ResolveAgent(cfg, "other")
	if len(ac.SessionInject) != 0 || !IsFallbackCapture(ac.SessionCapture) {
		t.Errorf("unknown cli-agent session defaults = %#v, want the generic capture and no inject", ac)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCodexTUIExitSessionCapture pins the two session-id shapes codex emits that
// the built-in session_capture must extract with ONE capture group (PTY-01 §四):
// the batch header `session id: <uuid>` and the TUI exit banner
// `… codex resume <uuid>`. The TUI form is what makes an interactive codex job
// resumable at all — its id never appears in stdout.log.
//
// NOT sampled on a live codex TUI (0.155.1 refuses to start in a non-interactive
// terminal without a trust prompt answered): the two forms below are the
// documented ones (design §四), and the ANSI decoration is stripped before the
// regex runs (see ptyrelay.Transcript / httpapi.ptySessionCapture).
func TestCodexTUIExitSessionCapture(t *testing.T) {
	reSrc := builtinSessionDefaults["codex"].SessionCapture
	re, err := regexp.Compile(reSrc)
	if err != nil {
		t.Fatalf("compile %q: %v", reSrc, err)
	}
	const sid = "0199f2c1-7a44-7b1e-9f10-2b6c9d0a1e33"
	samples := map[string]string{
		"batch header":   "codex exec\nsession id: " + sid + "\nworking…\n",
		"tui exit short": "bye\ncodex resume " + sid + "\n",
		"tui exit long":  "To continue this session, run codex resume " + sid + "\n",
		"tui exit caps":  "Resume this session with: codex resume " + sid + "\n",
	}
	for name, sample := range samples {
		m := re.FindStringSubmatch(sample)
		if len(m) < 2 || m[1] != sid {
			t.Fatalf("%s: regex did not extract the session id from %q (got %#v)", name, sample, m)
		}
	}
	for _, miss := range []string{
		"session id: not-a-uuid",
		"codex resume\n",
		"no session line at all\n",
	} {
		if got := re.FindStringSubmatch(miss); got != nil {
			t.Fatalf("regex matched a non-session line %q: %#v", miss, got)
		}
	}
}

// firstNonEmptyGroup mirrors the extractor contract internal/job.CaptureSessionIDBytes
// implements (PTY-01 F6): a multi-branch capture regex gives each alternative its own
// group and only ONE of them fires per match, so the id is the first NON-EMPTY group —
// never blindly group 1.
func firstNonEmptyGroup(m []string) string {
	if len(m) < 2 {
		return ""
	}
	for _, g := range m[1:] {
		if s := strings.TrimSpace(g); s != "" {
			return s
		}
	}
	return ""
}

// TestOmpTUIExitSessionCapture pins the shape an interactive omp TUI prints on exit
// (real sample, PTY-01 2026-09-22, job 20260922-164858-a7256a59):
//
//	Resume this session with omp --resume 01a0c84d-d444-72ca-ad7c-edadbae32034
//
// It is the ONLY session id an interactive omp job ever prints — the ndjson
// `"type":"session"` row needs `--mode json`, which a TUI job does not use — so the
// built-in capture regex must recognize BOTH shapes under the first-non-empty-group
// contract. The ANSI decoration around the line must not matter: the samples carry
// the raw escapes and the de-ANSI'd `[<u` / `[>4;0m` residue seen in pty.txt.
func TestOmpTUIExitSessionCapture(t *testing.T) {
	reSrc := builtinSessionDefaults["omp"].SessionCapture
	re, err := regexp.Compile(reSrc)
	if err != nil {
		t.Fatalf("compile %q: %v", reSrc, err)
	}
	const sid = "01a0c84d-d444-72ca-ad7c-edadbae32034"
	samples := map[string]string{
		"tui exit":            "Resume this session with omp --resume " + sid + "\n",
		"tui exit ansi":       "\x1b[<u\x1b[>4;0mResume this session with omp --resume " + sid + "\x1b[0m\r\n",
		"tui exit ansi noise": "[<u[>4;0mResume this session with omp --resume " + sid + "\n",
		"ndjson session row":  `{"type":"session","id":"` + sid + `"}`,
		"ndjson row ansi":     "\x1b[2K{\"type\":\"session\",\"id\":\"" + sid + "\"}\r\n",
	}
	for name, sample := range samples {
		if got := firstNonEmptyGroup(re.FindStringSubmatch(sample)); got != sid {
			t.Fatalf("%s: capture = %q, want %q (sample %q)", name, got, sid, sample)
		}
	}
	for _, miss := range []string{
		"Resume this session with omp --resume\n",
		"omp --resume not-a-uuid\n",
		`{"type":"session"}`,
		"no session line at all\n",
	} {
		if got := firstNonEmptyGroup(re.FindStringSubmatch(miss)); got != "" {
			t.Fatalf("regex matched a non-session line %q: %q", miss, got)
		}
	}
}

// TestClaudeTUIExitSessionCapture pins claude's TUI exit banner, the last two lines
// the TUI prints (real sample, PTY-01 2026-09-22, tty-claude job
// 20260922-163846-31abe0ab):
//
//	Resume this session with:
//	claude --resume 7c4418ff-0928-4e33-8347-c24241d919c0
//
// A claude job normally already carries its id — session_inject --session-id is
// appended on the TUI argv too — so this capture is the fallback on the agent entry
// that owns claude's session config; the exit banner is the only place the id would
// ever show up in the pty transcript.
func TestClaudeTUIExitSessionCapture(t *testing.T) {
	reSrc := builtinSessionDefaults["claude"].SessionCapture
	re, err := regexp.Compile(reSrc)
	if err != nil {
		t.Fatalf("compile %q: %v", reSrc, err)
	}
	const sid = "7c4418ff-0928-4e33-8347-c24241d919c0"
	samples := map[string]string{
		"two lines":       "Resume this session with:\nclaude --resume " + sid + "\n",
		"two lines ansi":  "\x1b[1mResume this session with:\x1b[0m\r\n\x1b[2mclaude --resume " + sid + "\x1b[0m\r\n",
		"one line":        "Resume this session with: claude --resume " + sid + "\n",
		"uppercase flags": "Resume this session with:\nClaude --Resume " + sid + "\n",
	}
	for name, sample := range samples {
		if got := firstNonEmptyGroup(re.FindStringSubmatch(sample)); got != sid {
			t.Fatalf("%s: capture = %q, want %q (sample %q)", name, got, sid, sample)
		}
	}
	for _, miss := range []string{
		"Resume this session with:\n",
		"claude --resume\n",
		"claude --resume not-a-uuid\n",
		"codex resume " + sid + "\n", // codex's banner is NOT claude's capture
	} {
		if got := firstNonEmptyGroup(re.FindStringSubmatch(miss)); got != "" {
			t.Fatalf("claude capture matched a non-session line %q: %q", miss, got)
		}
	}
}

// newFallbackAgent resolves a cli-agent that is NOT in the built-in table (key
// "jcode", no session_* config) and therefore gets the generic AGT-04 fallback.
func newFallbackAgent(t *testing.T) config.AgentConfig {
	t.Helper()
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"jcode": {Type: TypeCLIAgent, Command: "jcode", InteractiveArgs: []string{}},
	}}
	ac, ok := ResolveAgent(cfg, "jcode")
	if !ok {
		t.Fatal(`ResolveAgent("jcode") = not found`)
	}
	if !IsFallbackCapture(ac.SessionCapture) {
		t.Fatalf("jcode SessionCapture = %q, want the generic fallback", ac.SessionCapture)
	}
	return ac
}

// TestFallbackCaptureAcceptsNonUUIDToken is the AGT-04 acceptance case: a cli-agent
// the built-in table has never heard of (jcode, OpenCode Go 系) must still capture
// its session id with NO session_capture configured. The id is not a uuid, so all
// three built-in regexes miss it. Sample is the real exit banner as it reaches the
// capture (job 20260922-201228-becdd0e9), de-ANSI'd by ptyrelay but with the
// `[<u` / `[>4;0m` residue still in place.
func TestFallbackCaptureAcceptsNonUUIDToken(t *testing.T) {
	ac := newFallbackAgent(t)
	re, err := regexp.Compile(ac.SessionCapture)
	if err != nil {
		t.Fatalf("compile %q: %v", ac.SessionCapture, err)
	}
	const sid = "session_hamster_1790079148520_bc5cb0d44153fe56"
	sample := "[<u[>4;0mSession hamster - to resume:\n  jcode --resume " + sid + "[>4;0m\n"
	if got := firstNonEmptyGroup(re.FindStringSubmatch(sample)); got != sid {
		t.Fatalf("capture = %q, want %q (sample %q)", got, sid, sample)
	}
}

// TestFallbackCaptureAcceptsUUIDAndPrefixedIDs pins the three other id shapes the
// fallback exists to cover: a plain uuid (claude-like), a prefixed ULID, and the
// `--flag=value` spelling.
func TestFallbackCaptureAcceptsUUIDAndPrefixedIDs(t *testing.T) {
	ac := newFallbackAgent(t)
	re, err := regexp.Compile(ac.SessionCapture)
	if err != nil {
		t.Fatalf("compile %q: %v", ac.SessionCapture, err)
	}
	for _, tc := range []struct{ name, line, want string }{
		{"uuid", "foo --resume 7c4418ff-0928-4e33-8347-c24241d919c0", "7c4418ff-0928-4e33-8347-c24241d919c0"},
		{"prefixed ulid", "bar --resume ses_01H9XK2M3N4P5Q6R7S8T9V0W1X", "ses_01H9XK2M3N4P5Q6R7S8T9V0W1X"},
		{"equals form", "baz --session-id=abc12345", "abc12345"},
	} {
		if got := firstNonEmptyGroup(re.FindStringSubmatch(tc.line)); got != tc.want {
			t.Errorf("%s: capture = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestBuiltinKeyBeatsFallback: an agent the built-in table DOES know keeps exactly
// its built-in regex — the fallback must never widen what claude/codex/omp capture
// (their ids are pinned by PTY-01 and a looser pattern could grab an unrelated
// token instead).
func TestBuiltinKeyBeatsFallback(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"claude": {Type: TypeCLIAgent, Command: "claude"},
		"codex":  {Type: TypeCLIAgent, Command: "codex"},
		"omp":    {Type: TypeCLIAgent, Command: "omp", InteractiveArgs: []string{}},
	}}
	for _, key := range []string{"claude", "codex", "omp"} {
		ac, ok := ResolveAgent(cfg, key)
		if !ok {
			t.Fatalf("%s not found", key)
		}
		if want := builtinSessionDefaults[key].SessionCapture; ac.SessionCapture != want {
			t.Errorf("%s SessionCapture = %q, want the built-in %q", key, ac.SessionCapture, want)
		}
		if IsFallbackCapture(ac.SessionCapture) {
			t.Errorf("%s resolved to the generic fallback, want the built-in regex", key)
		}
	}
}

// TestExplicitCaptureBeatsFallback: an operator-written session_capture wins, and
// must not be reported as the fallback (the event detail and the tail-only window
// rule both key off that distinction).
func TestExplicitCaptureBeatsFallback(t *testing.T) {
	const mine = `mybanner:\s*(\S+)`
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"jcode": {Type: TypeCLIAgent, Command: "jcode", InteractiveArgs: []string{}, SessionCapture: mine},
	}}
	ac, _ := ResolveAgent(cfg, "jcode")
	if ac.SessionCapture != mine {
		t.Fatalf("explicit session_capture overwritten: %q", ac.SessionCapture)
	}
	if IsFallbackCapture(ac.SessionCapture) {
		t.Fatal("an explicit capture must not be reported as the fallback")
	}
}

// TestFallbackResumeTemplateApplied: capturing an id is only half of it — a
// cli-agent with no session_resume must also be able to `job resume`. Both
// templates are the shape claude/omp/jcode share; an explicit one still wins.
func TestFallbackResumeTemplateApplied(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		// interactive_args: [] is how a config says "this agent also runs in a pty"
		// (AGT-02) — the real jcode is registered that way.
		"jcode": {Type: TypeCLIAgent, Command: "jcode", InteractiveArgs: []string{}},
		"mine":  {Type: TypeCLIAgent, Command: "mine", SessionResume: []string{"-r", "{{session_id}}"}},
		// A batch-only cli-agent: it can still be resumed as a job, but it has no
		// terminal session, so no interactive template is invented for it (a session
		// takeover must keep reporting that there is nothing to run).
		"batchonly": {Type: TypeCLIAgent, Command: "batchonly"},
	}}
	jcode, _ := ResolveAgent(cfg, "jcode")
	wantBatch := []string{"--resume", "{{session_id}}", "-p", "{{prompt}}"}
	if !equalStringSlices(jcode.SessionResume, wantBatch) {
		t.Errorf("SessionResume = %#v, want %#v", jcode.SessionResume, wantBatch)
	}
	wantTUI := []string{"--resume", "{{session_id}}"}
	if !equalStringSlices(jcode.SessionResumeInteractive, wantTUI) {
		t.Errorf("SessionResumeInteractive = %#v, want %#v", jcode.SessionResumeInteractive, wantTUI)
	}

	mine, _ := ResolveAgent(cfg, "mine")
	if !equalStringSlices(mine.SessionResume, []string{"-r", "{{session_id}}"}) {
		t.Errorf("explicit SessionResume overwritten: %#v", mine.SessionResume)
	}

	batch, _ := ResolveAgent(cfg, "batchonly")
	if !equalStringSlices(batch.SessionResume, wantBatch) {
		t.Errorf("batch-only SessionResume = %#v, want %#v", batch.SessionResume, wantBatch)
	}
	if len(batch.SessionResumeInteractive) != 0 {
		t.Errorf("batch-only SessionResumeInteractive = %#v, want none", batch.SessionResumeInteractive)
	}
}

// TestFallbackSkipsExecAndACP: the fallback is a cli-agent concept only. An exec
// agent's argv belongs to the caller and an acp-agent's session travels over the
// protocol, so neither gets a session_capture to scan for or a resume argv.
func TestFallbackSkipsExecAndACP(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"exec":      {Type: TypeExec},
		"jcode-acp": {Type: TypeACPAgent, Command: "jcode", Args: []string{"acp"}},
	}}
	for _, key := range []string{"exec", "jcode-acp"} {
		ac, ok := ResolveAgent(cfg, key)
		if !ok {
			t.Fatalf("%s not found", key)
		}
		if ac.SessionCapture != "" || len(ac.SessionResume) != 0 || len(ac.SessionResumeInteractive) != 0 || len(ac.SessionInject) != 0 {
			t.Errorf("%s gained session_* fallback: %#v", key, ac)
		}
	}
}
