package config

import (
	"slices"
	"strings"
)

// FieldPolicy is one config field's write contract for the console (WEB-04③ V1.1,
// design §一.3): whether the write endpoints accept it, whether a change needs a
// process restart, and whether the field is a secret REFERENCE (an env-var name)
// rather than a value.
//
// ONE table, two consumers: the write endpoint refuses a field whose policy is not
// Editable with `field not editable: <path>`, and GET /v1/config publishes the same
// table so the console can disable those inputs and badge them. Two hand-maintained
// lists would drift, and the drift is invisible until an operator edits a field the
// server then refuses (or, worse, accepts and cannot apply).
type FieldPolicy struct {
	// Editable: PUT accepts this field.
	Editable bool
	// RestartRequired: the value is read once at process start (the listen address,
	// the storage root) or is secret material, so it can neither be applied by
	// Core.Update's hot reload nor safely round-tripped through a request body. It is
	// therefore NOT editable through the API and the console shows it as disabled.
	// This is the CONSERVATIVE class: a field that is simply not part of the V1.1
	// whitelist is classified here rather than guessed at, because "the console says
	// 需重启 for something that in fact reloads" costs a needless restart while the
	// reverse costs a silent no-op edit.
	RestartRequired bool
	// SecretRef: the field holds the NAME of an environment variable that carries the
	// secret (`token_env`), never the value itself. The distinction is what lets a
	// `*_env` name be shown and named while a literal is refused outright.
	SecretRef bool
}

// fieldPolicies is the explicit table. It is deliberately NOT derived by reflection:
// a new field must be classified by a human before the console offers it, and
// TestEveryServerFieldHasPolicy fails until it is.
//
// Paths are dotted yaml paths. An AGENT key is written `*`
// (`agents.*.command`), so every agent shares one set of rules. Only the fields a
// write endpoint can actually carry have entries here: the table IS the request
// vocabulary, which is why `GET /v1/config`'s per-field policy view and the PUT
// whitelist cannot disagree.
var fieldPolicies = map[string]FieldPolicy{
	// --- server: hot-editable (design §一.3 whitelist) --------------------------
	"server.max_job_timeout_sec": {Editable: true},
	"server.auto_resume_max":     {Editable: true},
	"server.stall_timeout_sec":   {Editable: true},
	// JOB-10 skills: the deployment's default binding list and the import size caps.
	// Both are read where they are used (EffectiveSkills runs per dispatch, the store
	// reads its limits at import), so a hot edit applies to the NEXT job/import —
	// nothing derives a cached value from them at startup.
	"server.skills":       {Editable: true},
	"server.skill_limits": {Editable: true},
	// Compound blocks are editable as a WHOLE (the request carries the block; a
	// partial block would silently drop the members it does not mention). None of
	// them carries a secret VALUE — a webhook holds a secret_env NAME.
	"server.notification": {Editable: true},
	"server.runner_probe": {Editable: true},
	"server.retry":        {Editable: true},
	// MCP-05: the @-mention dispatch throttle. Read per comment
	// (EffectiveCommentTrigger), so a hot edit applies to the NEXT comment.
	"server.comment_trigger": {Editable: true},
	// JOB-11 / SUP-01 P3 (S2, 2026-09-23): both are read where they are USED, from
	// the config generation the reader holds — dir_lock once per submit
	// (resolveDirExclusive) and agent_health once per health read
	// (EffectiveAgentHealth: the /v1/agents view, the pre-dispatch check and the
	// fallback decision). Neither is copied into a component at startup, so a hot
	// edit applies to the next job / the next health read.
	"server.dir_lock":     {Editable: true},
	"server.agent_health": {Editable: true},
	// SEC-01: the extra env denylist. Read per job spawn (effectiveJobEnvDeny), so a
	// hot edit applies to the NEXT job — nothing copies it at startup.
	"server.job_env_denylist": {Editable: true},
	// TUN-03: the forwarder registration TTL. The httpapi forwarder registry reads it
	// through a func on every read/write (Config.EffectiveForwarderTTL), so a hot edit
	// applies to the NEXT registration or listing — nothing caches the duration.
	"server.tunnel": {Editable: true},

	// --- supervisor: read where they are used ----------------------------------
	// MCP-05 阶段 B: the leader block is resolved per wake (config.LeaderConfig, i.e.
	// one snapshot per round), so editing the file + SIGHUP applies to the NEXT round —
	// nothing caches an agent/cap at construction. There is no console FORM for this
	// block yet (the write API's vocabulary is server|agents), so the entry is a
	// classification: the console badges it as a live (hot) field rather than as one
	// that needs a restart, which is what an operator editing the yaml needs to know.
	"supervisor.leader": {Editable: true},

	// --- server: read at startup, or secret material ---------------------------
	"server.addr":                   {RestartRequired: true},
	"server.token":                  {RestartRequired: true},
	"server.token_env":              {RestartRequired: true, SecretRef: true},
	"server.allow_empty_token":      {RestartRequired: true},
	"server.path_view":              {RestartRequired: true},
	"server.callers":                {RestartRequired: true},
	"server.web_enabled":            {RestartRequired: true},
	"server.web_dir":                {RestartRequired: true},
	"server.workers":                {RestartRequired: true},
	"server.metrics":                {RestartRequired: true},
	"server.governance":             {RestartRequired: true},
	"server.web_base_url":           {RestartRequired: true},
	"server.job_recover_window_sec": {RestartRequired: true},
	"server.agent_fallback":         {RestartRequired: true},
	// server.xfer stays restart-only (S2 verified 2026-09-23): core.Build resolves
	// its three caps ONCE into the transfer manager, which keeps them in an immutable
	// field (xfer.Manager.limits) and is NOT rebuilt by a config reload — accepting a
	// write here would report success and change nothing until the process restarts.
	"server.xfer": {RestartRequired: true},

	// --- agents: everything a definition needs, minus the secret-bearing `env` --
	"agents.*.type":                       {Editable: true},
	"agents.*.command":                    {Editable: true},
	"agents.*.args":                       {Editable: true},
	"agents.*.interactive_args":           {Editable: true},
	"agents.*.interactive":                {Editable: true},
	"agents.*.read_only_args":             {Editable: true},
	"agents.*.session_inject":             {Editable: true},
	"agents.*.session_capture":            {Editable: true},
	"agents.*.session_resume":             {Editable: true},
	"agents.*.session_resume_interactive": {Editable: true},
	"agents.*.system_inject":              {Editable: true},
	"agents.*.transient_error_patterns":   {Editable: true},
	"agents.*.fallback_agents":            {Editable: true},
	"agents.*.skills":                     {Editable: true},
	"agents.*.max_concurrent":             {Editable: true},
	"agents.*.stall_timeout_sec":          {Editable: true},
	"agents.*.retry":                      {Editable: true},
	"agents.*.acp":                        {Editable: true},
	"agents.*.output_format":              {Editable: true},
	"agents.*.ndjson_keep":                {Editable: true},
	"agents.*.ndjson_raw":                 {Editable: true},
	"agents.*.ndjson_events_to":           {Editable: true},
	"agents.*.ndjson_stdout":              {Editable: true},
	"agents.*.ndjson_stdout_path":         {Editable: true},
	"agents.*.ndjson_fields":              {Editable: true},
	// SEC-01: who may submit. Both are read where the decision is taken (the submit
	// permission check reads the asking job's agent/role definition from the live
	// config), so a hot edit applies to the next submit.
	"agents.*.can_submit":    {Editable: true},
	"agents.*.submit_agents": {Editable: true},

	// Read-only by decision (design §一.2), NOT by omission: `env` may carry a
	// plaintext secret and lands in request_json, `detect` probes the host, and
	// `mcp_server_name`/`allow_raw_cmd`/`no_raw_cmd` are operator plumbing that a web
	// form has no business flipping. A PUT preserves them (see the endpoint).
	"agents.*.env":             {},
	"agents.*.detect":          {},
	"agents.*.allow_raw_cmd":   {},
	"agents.*.no_raw_cmd":      {},
	"agents.*.mcp_server_name": {},
}

// FieldPolicyFor resolves a dotted config path to its policy. A path that resolves
// to no entry is a MISS (ok=false) — never a silent "not editable" default, because
// the write endpoint must be able to tell "you cannot change this" from "there is no
// such field", and the console from "not classified yet" from "classified read-only".
//
// The agent key is a wildcard: `agents.claude.command` and `agents.*.command` are
// the same lookup, and a nested path (`agents.claude.acp.modes`) resolves through its
// longest known prefix (`agents.*.acp`).
func FieldPolicyFor(path string) (FieldPolicy, bool) {
	if fp, ok := fieldPolicies[path]; ok {
		return fp, true
	}
	parts := strings.Split(path, ".")
	if len(parts) < 3 || parts[0] != "agents" {
		return FieldPolicy{}, false
	}
	for end := len(parts); end >= 3; end-- {
		if fp, ok := fieldPolicies["agents.*."+strings.Join(parts[2:end], ".")]; ok {
			return fp, true
		}
	}
	return FieldPolicy{}, false
}

// SectionPolicies returns one section's field policies keyed by the FIELD NAME a
// write body carries — `server` → "addr", "max_job_timeout_sec", …; `agents` → every
// agent field. That is exactly the vocabulary PUT accepts and the console builds its
// form from, so the view and the endpoint cannot offer different fields. An unknown
// section returns an empty map.
func SectionPolicies(section string) map[string]FieldPolicy {
	var prefix string
	switch section {
	case "server":
		prefix = "server."
	case "agents":
		prefix = "agents.*."
	case "supervisor":
		prefix = "supervisor."
	default:
		return map[string]FieldPolicy{}
	}
	out := make(map[string]FieldPolicy, len(fieldPolicies))
	for path, fp := range fieldPolicies {
		if name, ok := strings.CutPrefix(path, prefix); ok {
			out[name] = fp
		}
	}
	return out
}

// EditableAgentFields returns the agent field names PUT accepts, sorted. It is the
// whitelist the write endpoint enforces and the field list the console builds its
// form from — one source for both, so a form can never offer an input the server
// refuses.
func EditableAgentFields() []string {
	out := make([]string, 0, len(fieldPolicies))
	for path, fp := range fieldPolicies {
		if !fp.Editable {
			continue
		}
		if name, ok := strings.CutPrefix(path, "agents.*."); ok {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// secretFieldMarkers are the name fragments that mark a request field as a secret
// LITERAL. They are matched case-insensitively against the field NAME — a body key,
// not a config path — because the point is to catch the classic mistake (pasting a
// token into the console) before it ever reaches the config.
var secretFieldMarkers = []string{"token", "secret", "password", "passwd", "api_key", "apikey"}

// SecretFields returns those markers. The handler refuses a body field that matches
// one and points at the `*_env` alternative; the console uses the same list to label
// a secret field "由 env 提供".
func SecretFields() []string { return slices.Clone(secretFieldMarkers) }

// IsSecretFieldName reports whether a request field name denotes a secret LITERAL
// and must be refused: it carries one of SecretFields' markers and does NOT end in
// `_env`. The `_env` suffix is exactly what separates the two halves of the rule in
// design §一.2 — `token_env` names an environment variable and carries no value, so
// it is never treated as a secret (whether it is editable is a separate, per-field
// question).
func IsSecretFieldName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || strings.HasSuffix(lower, "_env") {
		return false
	}
	for _, marker := range secretFieldMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
