package cjkscan

import (
	"sort"
	"testing"
)

// Exemption classes of the tree-wide table (design 2026-10-27-i18n-design.md §11, S3d).
const (
	// classResidue: a package already migrated to internal/i18n that still holds a few
	// literals of its own (display text with a stated reason for staying).
	classResidue = "1-residue"
	// classProtocol: a literal the code matches or emits byte-exactly (old message
	// prefixes, recognised legacy prompts, keywords); removing it breaks recognition (§5).
	classProtocol = "2-protocol"
	// classStored: a persisted / generated-document template (SQL, the go-generate
	// reference doc, rows written with a fixed text); the text is data, not a response.
	classStored = "3-stored"
	// classDisplay: server-side display text for a person that has not been migrated yet.
	classDisplay = "4-display"
	// classPrompt: text written for an agent (prompts, prompt material labels) not migrated yet.
	classPrompt = "5-prompt"
	// classCLI: CLI text (stage 2 should have moved it; what is left is listed here).
	classCLI = "6-cli"
	// classData: bilingual data tables, where the Chinese literal is one half of a translation.
	classData = "7-bilingual-data"
)

// guardAllExempt is THE exemption table of the "no hardcoded Chinese in Go" guard: every
// non-test .go file under internal/ (except internal/i18n and internal/testutil) that
// still holds a string literal with a Han character, keyed by the path relative to
// internal/. Max is exact (a ceiling AND a floor): new Chinese turns the test red, and
// migrating text out turns it red until the entry is lowered / deleted — the table only
// ever gets shorter. Per-package guard tests were folded into this table in S3d.
var guardAllExempt = map[string]Exempt{
	// ---- migrated packages: leftovers -------------------------------------------------
	"desktop/toastps.go": {Max: 1, Class: classResidue, Why: "toastDisabledHint is an operator hint carried by a SkipError reason (logged once per cause, never shown in a popup), naming the Windows settings page in the Windows UI language of the pilot deployment; no language reaches the notifier's skip path yet"},

	"httpapi/authz_gen.go":                {Max: 51, Class: classStored, Why: "the template of the generated docs/reference/permissions.md (go generate): a Chinese reference document, not a response; the document language is an authoring choice"},
	"httpapi/agent_probe_handler.go":      {Max: 1, Class: classProtocol, Why: "the probe prompt sent to the agent under test, a fixed protocol line whose reply (\"OK\") is matched; not shown to a person"},
	"httpapi/runner_messenger_handler.go": {Max: 2, Class: classStored, Why: "title / target of the session-listing job recorded in the job store (stored text, not a response); the job service has no per-request language"},

	"job/reviewhints.go": {Max: 1, Class: classProtocol, Why: "\"验证\" is a keyword matched against report headings (recognition, not output; the English words \"verif\" / \"verify\" sit beside it)"},
	"job/rules.go":       {Max: 1, Class: classProtocol, Why: "rulesPromptHeader: fixed literal recognised structurally by the skills-manifest insertion and by older stored prompts; changing it breaks recognition"},

	"mail/address.go": {Max: 1, Class: classResidue, Why: "agent:<名字>[@<项目>] inside an English address parse error: the placeholders are the address grammar as the docs and the CLI help spell it (ParseAddress has no language; the error is returned to the sender through err.Error())"},
	"mail/service.go": {Max: 1, Class: classResidue, Why: "the term \"退信\" inside the English ErrNotAnswerable message (an error returned to the caller through err.Error(); sentinel + code is a separate migration)"},

	"notify/catalog.go": {Max: 46, Class: classData, Why: "the event catalog is bilingual data: every entry carries its Zh and En text side by side (the console picks one by language), so the Chinese literals are one half of a translation table, not untranslated output"},

	"sessionrelay/message.go": {Max: 1, Class: classProtocol, Why: "messengerSentMarkers[0]: the \"已发送\" receipt the zh messenger prompt asks the messenger agent to print; matched anywhere in its output (an English messenger prints \"sent\"), so the literal is recognition, not output"},

	"work/x2.go": {Max: 1, Class: classProtocol, Why: "completionBy is the stored author identifier of the completion write-back suggestion; changing it would split old and new rows"},

	// ---- protocol constants -----------------------------------------------------------
	"msgprefix/msgprefix.go": {Max: 27, Class: classProtocol, Why: "the message-prefix alias table: zh-CN forms of [gofer · …] / [来自 web …] prefixes that older stored messages and agents carry; recognised byte-exactly (design §5)"},
	"brief/issue.go":         {Max: 1, Class: classProtocol, Why: "planPrefix regex recognising Chinese plan headings in issue text (input recognition, not output)"},
	"changelog/merge.go":     {Max: 1, Class: classProtocol, Why: "DefaultUnreleasedTitles: the Chinese \"unreleased\" heading CHANGELOG.md files carry; matched, not emitted"},

	// ---- stored / generated ------------------------------------------------------------
	"jobstore/store.go":                 {Max: 2, Class: classStored, Why: "Chinese comments inside the CREATE TABLE DDL strings (schema text, G057)"},
	"jobstore/work_level.go":            {Max: 1, Class: classStored, Why: "the one-off UPDATE that classifies journal lines written by an older binary: its LIKE patterns match the Chinese text those binaries stored (\"状态：\", \"拆分出 \", \"已合并到 \", \"合并了 \"), so they must stay Chinese"},
	"jobstore/work_dispatch_migrate.go": {Max: 1, Class: classStored, Why: "Chinese text inside a one-off UPDATE migration statement; historical data rewrite"},
	"tracker/sync.go":                   {Max: 1, Class: classStored, Why: "the note a sync conflict writes into the merged issue / memory: it lands in the repository files two machines merge, so its text must be byte-identical whatever each machine's language is"},

	// ---- not migrated: server display text (S3g: what is left is config-load diagnostics, which run before any language is known) -----------------------------------------------
	"config/mail.go":    {Max: 1, Class: classDisplay, Why: "config validation error: English text carrying the agent:<名字> address-grammar placeholder; config validates before any language is known and every other config error is English (not migrated)"},
	"config/overlay.go": {Max: 1, Class: classDisplay, Why: "config-load warning: English log line with a developer aside; config loads before any language is known (not migrated)"},

	// ---- CLI text ------------------------------------------------------------------------
	"client/client.go":         {Max: 1, Class: classCLI, Why: "token-scope hint appended to a client error"},
	"commands/worker_init.go":  {Max: 5, Class: classCLI, Why: "comments of the generated worker config file"},
	"daemon/daemon.go":         {Max: 2, Class: classCLI, Why: "daemon early-exit error"},
	"daemon/daemon_windows.go": {Max: 1, Class: classCLI, Why: "daemon warning on Windows"},
	"job/workflow/init.go":     {Max: 33, Class: classCLI, Why: "the YAML scaffolds (with Chinese comments) written by `gofer wf init`"},
	"hookrelay/install.go":     {Max: 9, Class: classCLI, Why: "install notes printed by `gofer hook install`"},
}

// TestNoNewChineseInGo is the tree-wide "no hardcoded Chinese" guard (design §7): it
// scans every non-test Go file under internal/ except internal/i18n (the catalog) and
// internal/testutil, and compares with guardAllExempt exactly.
func TestNoNewChineseInGo(t *testing.T) {
	hits := TreeHits(t, "../..", "i18n", "testutil")
	CheckHits(t, hits, guardAllExempt)
	known := map[string]bool{classResidue: true, classProtocol: true, classStored: true, classDisplay: true, classPrompt: true, classCLI: true, classData: true}
	keys := make([]string, 0, len(guardAllExempt))
	for k := range guardAllExempt {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !known[guardAllExempt[k].Class] {
			t.Errorf("%s: exemption needs a known Class, got %q", k, guardAllExempt[k].Class)
		}
	}
}

func TestTreeHitsSkipsAndKeys(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		writeFile(t, dir, rel, src)
	}
	write("a/x.go", "package a\nvar v = \"中\"\n")
	write("a/b/y.go", "package b\nvar v = \"文\"\n")
	write("skip/z.go", "package s\nvar v = \"跳\"\n")
	write("a/testdata/t.go", "package t\nvar v = \"数\"\n")
	got := TreeHits(t, dir, "skip")
	if len(got) != 2 || len(got["a/x.go"]) != 1 || len(got["a/b/y.go"]) != 1 {
		t.Fatalf("TreeHits = %v, want a/x.go and a/b/y.go only", got)
	}
}
