// Package mcptools is the single registry of gofer's OWN MCP tools (the gofer_* tools
// internal/mcpserver exposes) and of what each one does to state.
//
// It exists because two very different places need the same answer:
//   - internal/mcpserver turns every entry into the tool's MCP `annotations`
//     (readOnlyHint / destructiveHint / idempotentHint), so an MCP client can tell a
//     list/get tool from one that writes before it calls either;
//   - the approval gate (internal/runner/acp) auto-allows a permission request for one
//     of these read-only tools no matter what kind the agent reported, so a steward
//     asking gofer_work_list does not park on a human.
//
// The registry is deliberately here — a leaf package both may import — instead of in
// internal/mcpserver (an entry-layer package the runner must not depend on). It is a
// whitelist of exact names, never a name-prefix guess: a tool another MCP server names
// gofer_work_list is not gofer's, and stays gated.
package mcptools

import "strings"

// Class says how a tool treats state. The three values map onto the MCP annotation
// hints, and only ReadOnly is ever auto-approved by the approval gate.
type Class string

const (
	// ReadOnly tools list, read, search or compute; they change nothing a caller has
	// to confirm.
	ReadOnly Class = "read_only"
	// Write tools create or change state in an additive way (submit a job, append a
	// note, send a letter).
	Write Class = "write"
	// Destructive tools delete or overwrite stored data (remove a memory, overwrite
	// one, cancel a job, end a session, withdraw a letter).
	Destructive Class = "destructive"
)

// Tool is one entry: the tool's exact MCP name and its class.
type Tool struct {
	Name  string
	Class Class
	// Idempotent marks a write that leaves the same state when repeated with the same
	// arguments (a "set" rather than an "append"). Read-only tools are always
	// idempotent and do not need it.
	Idempotent bool
	// Advisory marks a WRITE that only records a suggestion for a person to accept or
	// ignore (gofer_today_advise, gofer_work_merge_suggest, gofer_memory_suggest). It is
	// still a Write — it stores a row — but it changes no state a caller has to confirm
	// and executes nothing, so the approval gate may run it without a human.
	Advisory bool
}

// registry is every gofer_* tool internal/mcpserver registers, in a stable order:
// reads first, then writes, grouped by area. A tool missing here makes its
// registration panic (mcpserver.annotate) and fails TestEveryToolIsClassified.
//
// Classification rule: ReadOnly only if the call cannot change state a caller would
// care about. gofer_session_mail_read marks the letter read — bookkeeping that only
// stops it being re-injected — so it stays ReadOnly; gofer_plan_handoff and
// gofer_steward_notes have a write action, so they are Write even though they can also
// read.
var registry = []Tool{
	// Jobs / projects / agents.
	{Name: "gofer_list_projects", Class: ReadOnly},
	{Name: "gofer_list_agents", Class: ReadOnly},
	{Name: "gofer_get_job", Class: ReadOnly},
	{Name: "gofer_tail_log", Class: ReadOnly},
	{Name: "gofer_get_interactions", Class: ReadOnly},
	{Name: "gofer_get_artifacts", Class: ReadOnly},
	{Name: "gofer_get_result", Class: ReadOnly},
	{Name: "gofer_list_templates", Class: ReadOnly},
	{Name: "gofer_list_pending_interactions", Class: ReadOnly},
	{Name: "gofer_list_jobs", Class: ReadOnly},

	// Plans / todos.
	{Name: "gofer_list_plans", Class: ReadOnly},
	{Name: "gofer_get_plan", Class: ReadOnly},

	// Comments / issue and plan briefs.
	{Name: "gofer_list_comments", Class: ReadOnly},
	{Name: "gofer_issue_brief", Class: ReadOnly},
	{Name: "gofer_plan_brief", Class: ReadOnly},
	{Name: "gofer_issue_list", Class: ReadOnly},
	{Name: "gofer_issue_get", Class: ReadOnly},

	// Wakeups.
	{Name: "gofer_wakeup_list", Class: ReadOnly},

	// Memories.
	{Name: "gofer_memory_list", Class: ReadOnly},
	{Name: "gofer_memory_get", Class: ReadOnly},
	{Name: "gofer_memory_candidates", Class: ReadOnly},
	{Name: "gofer_memory_findings", Class: ReadOnly},

	// Work items and sessions.
	{Name: "gofer_work_list", Class: ReadOnly},
	{Name: "gofer_work_get", Class: ReadOnly},
	{Name: "gofer_work_requests", Class: ReadOnly},
	{Name: "gofer_session_list", Class: ReadOnly},
	{Name: "gofer_session_get", Class: ReadOnly},
	{Name: "gofer_session_tail", Class: ReadOnly},
	{Name: "gofer_session_find", Class: ReadOnly},
	{Name: "gofer_session_mail_list", Class: ReadOnly},
	{Name: "gofer_session_mail_read", Class: ReadOnly},

	// Today.
	{Name: "gofer_today_list", Class: ReadOnly},
	{Name: "gofer_today_card", Class: ReadOnly},

	// Job writes.
	{Name: "gofer_run_job", Class: Write},
	{Name: "gofer_job_set", Class: Write, Idempotent: true},
	{Name: "gofer_cancel_job", Class: Destructive},
	{Name: "gofer_job_say", Class: Write},
	{Name: "gofer_job_end", Class: Destructive},
	{Name: "gofer_reject_job", Class: Write},
	{Name: "gofer_answer_interaction", Class: Write},
	{Name: "gofer_punt_interaction", Class: Write},
	{Name: "gofer_ask_human", Class: Write},

	// Plan / todo writes.
	{Name: "gofer_create_plan", Class: Write},
	{Name: "gofer_attach_job", Class: Write, Idempotent: true},
	{Name: "gofer_update_plan_tags", Class: Write, Idempotent: true},
	{Name: "gofer_set_plan_supervisor_session", Class: Write, Idempotent: true},
	{Name: "gofer_set_plan_notify", Class: Write, Idempotent: true},
	{Name: "gofer_add_todo", Class: Write},
	{Name: "gofer_update_todo", Class: Write, Idempotent: true},
	{Name: "gofer_dispatch_todo", Class: Write},
	{Name: "gofer_plan_run", Class: Write},
	{Name: "gofer_plan_handoff", Class: Write},

	// Comment / wakeup writes.
	{Name: "gofer_comment", Class: Write},
	{Name: "gofer_wakeup_create", Class: Write},
	{Name: "gofer_wakeup_disable", Class: Write, Idempotent: true},

	// Memory writes.
	{Name: "gofer_memory_set", Class: Destructive},
	{Name: "gofer_memory_rm", Class: Destructive},
	{Name: "gofer_memory_flag", Class: Write},
	{Name: "gofer_memory_candidate_adopt", Class: Write},
	{Name: "gofer_memory_candidate_reject", Class: Write},
	// Advisory: it only proposes a tidying (archive / merge / retype / summary), which
	// becomes a 「今天」 card the person adopts or ignores.
	{Name: "gofer_memory_suggest", Class: Write, Advisory: true},

	// Work-item / session writes.
	{Name: "gofer_work_update", Class: Write},
	{Name: "gofer_work_note", Class: Write},
	{Name: "gofer_work_report", Class: Write},
	{Name: "gofer_work_capture", Class: Write},
	{Name: "gofer_work_remind", Class: Write},
	// Advisory: it only records a merge suggestion; merging stays a person's call.
	{Name: "gofer_work_merge_suggest", Class: Write, Advisory: true},
	{Name: "gofer_work_request_report", Class: Write},
	{Name: "gofer_work_summarize", Class: Write},
	{Name: "gofer_steward_notes", Class: Write},
	{Name: "gofer_session_ask", Class: Write},
	{Name: "gofer_session_set_topic", Class: Write},
	{Name: "gofer_session_mail_send", Class: Write},
	{Name: "gofer_session_mail_reply", Class: Write},
	{Name: "gofer_session_mail_cancel", Class: Destructive},

	// Today writes.
	// Advisory: advice only — the person still decides whether to act on it, and the
	// card's action_id is a SUGGESTION gofer never runs on the steward's behalf.
	{Name: "gofer_today_advise", Class: Write, Advisory: true},
}

// byName indexes registry once, so the approval gate's lookup is a map hit on the hot
// path of every permission request.
var byName = func() map[string]Tool {
	m := make(map[string]Tool, len(registry))
	for _, t := range registry {
		m[t.Name] = t
	}
	return m
}()

// All returns the registry (a copy of the slice; the entries are values).
func All() []Tool { return append([]Tool(nil), registry...) }

// Lookup returns the entry for an exact gofer tool name.
func Lookup(name string) (Tool, bool) {
	t, ok := byName[name]
	return t, ok
}

// IsReadOnly reports whether name is one of gofer's read-only tools.
func IsReadOnly(name string) bool {
	t, ok := byName[name]
	return ok && t.Class == ReadOnly
}

// IsAdvisory reports whether name is one of gofer's advice-only write tools.
func IsAdvisory(name string) bool {
	t, ok := byName[name]
	return ok && t.Advisory
}

// AutoApprovedTools returns the exact names of the tools the approval gate may run
// without a human by DEFAULT under mode=ask (config.DefaultApprovalAutoAllowTools):
// every read-only tool, then every advisory one, in registry order. The list is data,
// not a prefix rule — a tool another MCP server names the same way is never included.
func AutoApprovedTools() []string {
	out := make([]string, 0, len(registry))
	for _, t := range registry {
		if t.Class == ReadOnly {
			out = append(out, t.Name)
		}
	}
	for _, t := range registry {
		if t.Advisory {
			out = append(out, t.Name)
		}
	}
	return out
}

// MailReplyTool is the mailbox's answer tool. It is a Write tool, but the resident 收件会话
// may call it without a human (gofer-sh7r): answering a letter that arrived in this session
// is the whole job. Callers still gate every other gofer write tool.
const MailReplyTool = "gofer_session_mail_reply"

// MailReplyToolName reports the gofer mailbox reply tool raw names, if it names one. It uses
// the same exact-name resolution as ToolName, so a tool qualified by another MCP server (or a
// name that only looks like it) is not gofer's and stays gated.
func MailReplyToolName(raw string) (string, bool) {
	name, ok := ToolName(raw)
	if !ok || name != MailReplyTool {
		return "", false
	}
	return name, true
}

// ServerName is the name gofer's own MCP server is advertised under when gofer injects it
// (the steward's and a resident 收件会话's session both get exactly this name). It is the
// canonical server segment of an MCP-qualified tool name, and the approval gate takes
// gofer's tool names only from it (or from a name this server itself advertised for the
// same gofer binary) — never from any other MCP server's namespace.
const ServerName = "gofer"

// ServerOf returns the server segment of an mcp__<server>__<tool> name ("gofer" for
// "mcp__gofer__gofer_work_list"). ok is false for a bare name and for any other
// namespacing scheme, which is what lets a caller tell "the agent said this is gofer's
// tool" from "the agent sent a bare name gofer cannot attribute".
func ServerOf(raw string) (string, bool) {
	name := normalize(raw)
	if !strings.HasPrefix(name, "mcp__") {
		return "", false
	}
	parts := strings.Split(name, "__")
	if len(parts) < 3 || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// BaseName returns the tool segment of a raw or mcp__<server>__<tool> name, lowercased
// and unquoted ("gofer_work_list" for both "gofer_work_list" and
// "mcp__other__gofer_work_list"). It deliberately does NOT say whether the name is
// gofer's: config's tool lists are matched through it for tools of ANY MCP server.
func BaseName(raw string) string {
	name := normalize(raw)
	if srv, ok := ServerOf(name); ok && srv != "" {
		parts := strings.Split(name, "__")
		return parts[len(parts)-1]
	}
	return name
}

// normalize lowercases, trims and unquotes a raw tool name.
func normalize(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	return strings.Trim(name, "\"'")
}

// ToolName resolves the gofer tool a raw name refers to. raw is either the bare tool
// name (what suga and most ACP agents put in a tool call's title, e.g.
// "gofer_work_list") or an MCP-qualified name in the mcp__<server>__<tool> shape
// ("mcp__gofer__gofer_work_list"). A name qualified by ANOTHER server is not gofer's
// tool even when the last segment matches, and anything else that looks namespaced is
// refused rather than guessed: the caller uses this to skip a human approval, so a
// false positive is the expensive mistake.
func ToolName(raw string) (string, bool) {
	name := normalize(raw)
	if name == "" {
		return "", false
	}
	if srv, qualified := ServerOf(name); qualified {
		if srv != ServerName {
			return "", false
		}
		name = BaseName(name)
	} else if strings.Contains(name, "__") {
		// A server-namespaced name in some other scheme: do not guess.
		return "", false
	}
	if _, ok := byName[name]; !ok {
		return "", false
	}
	return name, true
}
