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

	"httpapi/inbox_handler.go":            {Max: 1, Class: classResidue, Why: "home-page card title suffix (today cards): S3b scope (today / work / session page text)"},
	"httpapi/today_handler.go":            {Max: 1, Class: classResidue, Why: "home-page lane label suffix: S3b scope (today / work / session page text)"},
	"httpapi/session_handler.go":          {Max: 2, Class: classResidue, Why: "the session page's handed-off notice and its \"just now\": S3b scope (toSessionView / session page text)"},
	"httpapi/session_inject.go":           {Max: 1, Class: classResidue, Why: "status detail of an injected message whose job record is gone: S3b scope (session page text)"},
	"httpapi/work_dispatch_handler.go":    {Max: 2, Class: classResidue, Why: "work-item journal lines (dispatch results): S3b scope (work journal)"},
	"httpapi/work_handler.go":             {Max: 1, Class: classResidue, Why: "work-item journal line (archived from the capture box): S3b scope (work journal)"},
	"httpapi/work_wiring.go":              {Max: 10, Class: classResidue, Why: "the work summarizer host's refusal reasons, shown in the work settings: S3b scope (work)"},
	"httpapi/authz_gen.go":                {Max: 51, Class: classStored, Why: "the template of the generated docs/reference/permissions.md (go generate): a Chinese reference document, not a response; the document language is an authoring choice"},
	"httpapi/agent_probe_handler.go":      {Max: 1, Class: classProtocol, Why: "the probe prompt sent to the agent under test, a fixed protocol line whose reply (\"OK\") is matched; not shown to a person"},
	"httpapi/runner_messenger_handler.go": {Max: 2, Class: classStored, Why: "title / target of the session-listing job recorded in the job store (stored text, not a response); the job service has no per-request language"},

	"job/config.go":           {Max: 1, Class: classResidue, Why: "operator-facing config error (worker policy pending)"},
	"job/delivery_check.go":   {Max: 5, Class: classResidue, Why: "delivery-check progress lines recorded in the job's events for people"},
	"job/model.go":            {Max: 15, Class: classResidue, Why: "status labels shown to people (StatusLabel)"},
	"job/reviewhints.go":      {Max: 2, Class: classProtocol, Why: "\"验证\" is a keyword matched against todo text (recognition, not output) and the truncation note of a review hint"},
	"job/rules.go":            {Max: 1, Class: classProtocol, Why: "rulesPromptHeader: fixed literal recognised structurally by the skills-manifest insertion and by older stored prompts; changing it breaks recognition"},
	"job/session_recovery.go": {Max: 1, Class: classResidue, Why: "error text returned to the operator after a worker restart"},
	"job/submit.go":           {Max: 2, Class: classResidue, Why: "submit validation errors returned to the caller"},
	"job/todo_continue.go":    {Max: 23, Class: classResidue, Why: "reasons why a todo could not continue its predecessor's session, stored as a note for people"},
	"job/todolink.go":         {Max: 1, Class: classResidue, Why: "todo note appended when the linked job awaits review"},
	"job/worktree.go":         {Max: 2, Class: classResidue, Why: "worktree check error and the merge note"},

	"mail/address.go": {Max: 2, Class: classResidue, Why: "an address parse error returned to the sender and the offline hint of a recipient label"},
	"mail/origin.go":  {Max: 1, Class: classResidue, Why: "the \"(caller ...)\" suffix of a sender label shown in the console"},
	"mail/service.go": {Max: 3, Class: classResidue, Why: "a send error returned to the caller, the withdrawn-letter log line and the excerpt label of a notice"},

	"notify/catalog.go": {Max: 46, Class: classData, Why: "the event catalog is bilingual data: every entry carries its Zh and En text side by side (the console picks one by language), so the Chinese literals are one half of a translation table, not untranslated output"},

	"sessionrelay/deliver.go":           {Max: 4, Class: classResidue, Why: "delivery failure reasons reported to the caller"},
	"sessionrelay/deliver_raw.go":       {Max: 2, Class: classResidue, Why: "relay-job outcome lines recorded as the delivery detail"},
	"sessionrelay/deliver_reconcile.go": {Max: 6, Class: classResidue, Why: "reconcile outcome lines recorded as the delivery detail"},
	"sessionrelay/message.go":           {Max: 9, Class: classResidue, Why: "message dispatch failure reasons and the sent note"},
	"sessionrelay/nudge.go":             {Max: 1, Class: classResidue, Why: "the pause reason of a nudge after consecutive failures"},
	"sessionrelay/reply.go":             {Max: 1, Class: classResidue, Why: "truncation marker appended to a long reply"},

	"today/memory_suggest.go": {Max: 1, Class: classResidue, Why: "ErrMemorySuggestionStale: an error sentinel whose text httpapi returns as the error detail and stores in the suggestion note; error detail text is a separate migration"},

	"work/noise.go":             {Max: 4, Class: classResidue, Why: "draft-exclusion reasons that only reach the server log (slog), never a person or the web"},
	"work/steward.go":           {Max: 2, Class: classPrompt, Why: "labels inside the prompt material handed to the steward agent"},
	"work/summarize.go":         {Max: 22, Class: classPrompt, Why: "the summarizer agent's prompt and material labels, plus summarize status / failure reasons returned to the caller and stored in the request ledger (the reasons need the ctx plumbed through StartSummarize first)"},
	"work/summarize_session.go": {Max: 3, Class: classPrompt, Why: "retry nudge sent to the summarizer agent and its session errors"},
	"work/x2.go":                {Max: 1, Class: classProtocol, Why: "completionBy is the stored author identifier of the completion write-back suggestion; changing it would split old and new rows"},

	// ---- protocol constants -----------------------------------------------------------
	"msgprefix/msgprefix.go": {Max: 27, Class: classProtocol, Why: "the message-prefix alias table: zh-CN forms of [gofer · …] / [来自 web …] prefixes that older stored messages and agents carry; recognised byte-exactly (design §5)"},
	"brief/issue.go":         {Max: 1, Class: classProtocol, Why: "planPrefix regex recognising Chinese plan headings in issue text (input recognition, not output)"},
	"changelog/merge.go":     {Max: 1, Class: classProtocol, Why: "DefaultUnreleasedTitles: the Chinese \"unreleased\" heading CHANGELOG.md files carry; matched, not emitted"},

	// ---- stored / generated ------------------------------------------------------------
	"jobstore/store.go":                 {Max: 2, Class: classStored, Why: "Chinese comments inside the CREATE TABLE DDL strings (schema text, G057)"},
	"jobstore/work_dispatch_migrate.go": {Max: 1, Class: classStored, Why: "Chinese text inside a one-off UPDATE migration statement; historical data rewrite"},

	// ---- not migrated: server display text -----------------------------------------------
	"authz/catalog.go":                 {Max: 55, Class: classDisplay, Why: "risk / kind labels and permission descriptions shown in the console and the generated permissions doc"},
	"authz/pattern.go":                 {Max: 3, Class: classDisplay, Why: "subject-kind labels shown in the console"},
	"config/default_project.go":        {Max: 1, Class: classDisplay, Why: "ErrBuiltinProject error text"},
	"config/mail.go":                   {Max: 1, Class: classDisplay, Why: "config validation error with a Chinese placeholder name"},
	"config/overlay.go":                {Max: 1, Class: classDisplay, Why: "config overlay warning with a Chinese aside"},
	"delivery/step.go":                 {Max: 1, Class: classDisplay, Why: "unsupported-provider error of a built-in delivery step"},
	"hookrelay/transcript.go":          {Max: 1, Class: classDisplay, Why: "truncation marker of a relayed transcript message"},
	"jobstore/delete.go":               {Max: 1, Class: classDisplay, Why: "event title \"deleted\" stored in the delete event detail"},
	"jobstore/mail_secret.go":          {Max: 4, Class: classDisplay, Why: "caller / session labels of a mail sender"},
	"jobstore/sessions.go":             {Max: 1, Class: classDisplay, Why: "truncation suffix of a stored session message"},
	"jobstore/steward.go":              {Max: 2, Class: classDisplay, Why: "steward merge-suggestion note lines"},
	"jobstore/tracker_server_issue.go": {Max: 1, Class: classDisplay, Why: "issue-id-renamed comment written to the issue"},
	"jobstore/work.go":                 {Max: 37, Class: classDisplay, Why: "work-item journal / change-summary lines written by the store (stored, no language plumbed to the store)"},
	"jobstore/work_level.go":           {Max: 4, Class: classDisplay, Why: "work journal lines for answered decisions / interactions"},
	"jobstore/work_noise.go":           {Max: 1, Class: classDisplay, Why: "short-lived-session exclusion reason"},
	"runner/worker/runner.go":          {Max: 1, Class: classDisplay, Why: "error after a worker restart ended a persistent session"},
	"settings/settings.go":             {Max: 13, Class: classDisplay, Why: "setting descriptions shown in the settings page"},
	"webpush/dispatch.go":              {Max: 19, Class: classDisplay, Why: "Web Push notification titles / bodies / action labels"},
	"workbench/attachments.go":         {Max: 15, Class: classDisplay, Why: "attachment routing reasons shown in the console"},
	"workbench/diff.go":                {Max: 3, Class: classDisplay, Why: "diff errors and notice"},
	"workbench/review.go":              {Max: 1, Class: classDisplay, Why: "review turn error"},
	"workbench/service.go":             {Max: 2, Class: classDisplay, Why: "turn errors"},
	"wshub/hub.go":                     {Max: 1, Class: classDisplay, Why: "protocol-version refusal reason shown to the operator"},
	"wshub/upgrade.go":                 {Max: 1, Class: classDisplay, Why: "upgrade-unsupported error"},
	"tracker/sync.go":                  {Max: 1, Class: classDisplay, Why: "sync-conflict note written into a memory entry"},
	"work/transcript/transcript.go":    {Max: 4, Class: classPrompt, Why: "role labels and the elision marker of the transcript text handed to the summarizer agent"},

	// ---- not migrated: agent prompts -----------------------------------------------------
	"messenger/agents.go":    {Max: 1, Class: classPrompt, Why: "ListAgentsPrompt: the prompt sent to the agent to list agents (its reply format is parsed)"},
	"settings/prompt.go":     {Max: 1, Class: classPrompt, Why: "ExtraHeading separating the default prompt from the user's addition"},
	"tracker/memory_flag.go": {Max: 1, Class: classPrompt, Why: "MemoryFlagHint injected into the agent's context"},

	"job/workflow/submit.go":    {Max: 1, Class: classDisplay, Why: "unsupported-step error returned on workflow submit"},
	"job/workflow/templates.go": {Max: 7, Class: classPrompt, Why: "prompts of the built-in workflow templates (plan / implement / review / reviewer-committee verifier steps)"},

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
