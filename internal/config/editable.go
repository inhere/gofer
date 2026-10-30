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
	// HotReload means the running process applies changes on reload, while the
	// field remains outside the console write whitelist (for example worker
	// bindings contain credential-bearing configuration).
	HotReload bool
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
	// Project settings are classified here for policy inspection; the config
	// console currently has no project write endpoint, so YAML remains the owner.
	"projects.*.on_uncommitted":     {},
	"projects.*.uncommitted_ignore": {},
	// gofer-krzy: which tracker the injected `tracker-prime` comes from (repo|nearest|off).
	"projects.*.tracker_prime": {},
	// JOB-06②: which secrets a project's jobs may ask for — file-only (see "secrets").
	"projects.*.secrets": {},
	// How many consecutive plan items may share one agent session — file-only.
	"projects.*.continue_max_chain": {},
	// The language of the text gofer generates for this project's jobs (design
	// 2026-10-27-i18n-design.md §3) — file-only for now, like the other project keys.
	"projects.*.language": {},
	// --- server: hot-editable (design §一.3 whitelist) --------------------------
	"server.max_job_timeout_sec": {Editable: true},
	"server.auto_resume_max":     {Editable: true},
	"server.stall_timeout_sec":   {Editable: true},
	// JOB-10 skills: the deployment's default binding list and the import size caps.
	// Both are read where they are used (EffectiveSkills runs per dispatch, the store
	// reads its limits at import), so a hot edit applies to the NEXT job/import —
	// nothing derives a cached value from them at startup.
	"server.skills": {Editable: true},
	// MCP-06: the server-wide MCP binding list (a partial server write leaves it alone
	// when omitted); read per submit by EffectiveMCP.
	"server.mcp":          {Editable: true},
	"server.skill_limits": {Editable: true},
	// JOB-06① rules: the deployment's mandatory binding list and the total-size cap.
	// Both are read where they are used (EffectiveRules runs per dispatch,
	// EffectiveRulesMaxBytes per submit), so a hot edit applies to the NEXT job —
	// nothing derives a cached value from them at startup.
	"server.rules":           {Editable: true},
	"server.rules_max_bytes": {Editable: true},
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
	"server.dir_lock": {Editable: true},
	// F12 (2026-09-25): the directory-lock WAIT cap, read per submit
	// (EffectiveDirLockMaxWaitSec) like dir_lock itself — a hot edit applies to the
	// next job.
	"server.dir_lock_max_wait_sec": {Editable: true},
	// gofer-y05q: the worker capacity-queue cap is captured by each worker runner when it
	// is built (assemble, or a reload that ADDS a worker runner), so an existing runner
	// keeps the value it was built with — the conservative class rather than a hot edit.
	"server.worker_queue_max_wait_sec":     {RestartRequired: true},
	"server.dir_lock_allow_unbounded_wait": {Editable: true},
	"server.agent_health":                  {Editable: true},
	// SEC-01: the extra env denylist. Read per job spawn (effectiveJobEnvDeny), so a
	// hot edit applies to the NEXT job — nothing copies it at startup.
	"server.job_env_denylist": {Editable: true},
	// TUN-03: the forwarder registration TTL. The httpapi forwarder registry reads it
	// through a func on every read/write (Config.EffectiveForwarderTTL), so a hot edit
	// applies to the NEXT registration or listing — nothing caches the duration.
	"server.tunnel": {Editable: true},

	// --- leader: read where it is used -----------------------------------------
	// MCP-05 阶段 B: the leader block is resolved per wake (config.LeaderConfig, i.e.
	// one snapshot per round), so editing the file + SIGHUP applies to the NEXT round —
	// nothing caches an agent/cap at construction. There is no console FORM for this
	// block yet (the write API's vocabulary is server|agents), so the entry is a
	// classification: the console badges it as a live (hot) field rather than as one
	// that needs a restart, which is what an operator editing the yaml needs to know.
	"leader": {Editable: true},
	// Session relay preferences are applied by the serve reload hook and therefore
	// take effect on the next session without a process restart.
	"session": {Editable: true},
	// The mailbox deliverer's numbers are re-read by the serve reload hook (deliverer
	// settings and the delivery deadline of new letters), so an edit applies without a restart.
	"mail": {Editable: true},
	// The work digest settings are read by the digest loop on every tick.
	"work": {Editable: true},
	// The steward block is read per use (start / ask / tick), so an edit applies to the
	// next one; changing steward.agent ends the running steward session.
	"steward": {Editable: true},
	// CFG-16: the server-local runner's declared tools are read per submit (the
	// prompt section) and per /v1/runners read, so a reload applies to the next job.
	// They describe THIS machine, so the console does not write them.
	"tools": {HotReload: true},
	// JOB-06②: the secret registry maps a server variable to a job — file-only, or
	// anyone who can edit config could hand themselves any server variable.
	"secrets": {HotReload: true},
	// gofer-29gi: where the server's outbound traffic goes — file-only; read per request.
	"http_proxy": {HotReload: true},

	// --- server: read at startup, or secret material ---------------------------
	"server.addr":              {RestartRequired: true},
	"server.token":             {RestartRequired: true},
	"server.token_env":         {RestartRequired: true, SecretRef: true},
	"server.allow_empty_token": {RestartRequired: true},
	"server.path_view":         {RestartRequired: true},
	// The console's server form carries read_dir_allow on every save (gofer-x4h1), so
	// it must be editable: with HotReload alone every server save answered 400.
	"server.read_dir_allow": {Editable: true},
	"server.callers":        {RestartRequired: true},
	// Per-caller scope ceilings (上限): FILE-ONLY by decision — a ceiling is a limit the
	// operator sets on a token, and the console write endpoint's vocabulary is
	// server|agents, so no request can carry it. The judgment reads it from the live
	// config snapshot on every request / submit, so an edited file + reload applies at
	// once (serve swaps the whole Server block through SetServerConfig).
	"server.callers.*.ceiling": {HotReload: true},
	"server.web_enabled":       {RestartRequired: true},
	"server.web_dir":           {RestartRequired: true},
	"server.tls":               {RestartRequired: true},
	"server.workers":           {HotReload: true},
	// gofer-9b1b: read per held submit (EffectiveHoldTimeoutSec), so a reload applies
	// to the next held job; the console has no form for it yet (WP3/WP5).
	"server.hold":         {HotReload: true},
	"server.metrics":      {RestartRequired: true},
	"server.governance":   {RestartRequired: true},
	"server.web_base_url": {RestartRequired: true},
	// The deployment's text language (en | zh-CN): read per job submit / per text, so
	// a hot edit applies to the NEXT job — nothing caches it at startup.
	"server.language":               {Editable: true},
	"server.session_messaging":      {RestartRequired: true},
	"server.job_recover_window_sec": {RestartRequired: true},
	"server.policy_repush":          {RestartRequired: true},
	"server.agent_fallback":         {RestartRequired: true},
	"server.push":                   {RestartRequired: true}, // W2b: VAPID subject is read when the push service is wired at serve start
	// server.xfer stays restart-only (S2 verified 2026-09-23): core.Build resolves
	// its three caps ONCE into the transfer manager, which keeps them in an immutable
	// field (xfer.Manager.limits) and is NOT rebuilt by a config reload — accepting a
	// write here would report success and change nothing until the process restarts.
	"server.xfer": {RestartRequired: true},
	// The workbench attachment numbers are read per upload (Config.EffectiveAttachments through
	// the live config), so a file edit + reload applies to the NEXT upload. No console form yet
	// (the write switch has no case for it), hence HotReload rather than Editable.
	"server.attachments": {HotReload: true},
	// AUTO-04: event hooks run commands on the server host, so they are FILE-ONLY by
	// decision (the write API must refuse them — without require_admin_capability any
	// authenticated caller could otherwise run a command here). The delivery sweep
	// reads them per run, so a file edit + reload applies at once.
	"server.event_hooks": {HotReload: true},
	// AUTO-04b: decision hooks — file-only for the same reason; read per decision.
	"server.decision_hooks": {HotReload: true},
	// gofer-6x2n: the update source is fetched by the server — file-only.
	"server.update": {HotReload: true},
	// Self-service worker enrollment: the block is console-editable and read per request;
	// the proxy list decides whose forwarded-for header is believed, so it is file-only.
	"server.worker_enrollment":                 {Editable: true, HotReload: true},
	"server.worker_enrollment.trusted_proxies": {HotReload: true},

	// --- agents: everything a definition needs, minus the secret-bearing `env` --
	"agents.*.type":                       {Editable: true},
	"agents.*.command":                    {Editable: true},
	"agents.*.args":                       {Editable: true},
	"agents.*.global_args":                {Editable: true},
	"agents.*.interactive_args":           {Editable: true},
	"agents.*.interactive":                {Editable: true},
	"agents.*.read_only_args":             {Editable: true},
	"agents.*.add_dir_args":               {Editable: true},
	"agents.*.session_inject":             {Editable: true},
	"agents.*.exit_keys":                  {Editable: true},
	"agents.*.exit_grace_sec":             {Editable: true},
	"agents.*.session_store_glob":         {Editable: true},
	"agents.*.session_store_id_regex":     {Editable: true},
	"agents.*.session_capture":            {Editable: true},
	"agents.*.session_resume":             {Editable: true},
	"agents.*.session_resume_interactive": {Editable: true},
	"agents.*.system_inject":              {Editable: true},
	"agents.*.transient_error_patterns":   {Editable: true},
	"agents.*.fallback_agents":            {Editable: true},
	"agents.*.skills":                     {Editable: true},
	// MCP-06: the agent form round-trips all three (the write REPLACES the editable set).
	"agents.*.mcp":               {Editable: true},
	"agents.*.mcp_format":        {Editable: true},
	"agents.*.mcp_args":          {Editable: true},
	"agents.*.rules":             {Editable: true},
	"agents.*.max_concurrent":    {Editable: true},
	"agents.*.stall_timeout_sec": {Editable: true},
	"agents.*.retry":             {Editable: true},
	// The acp block, written as one PATCH body: its members (`modes`,
	// `permission_policy`, `load_session`, `log_thoughts`, `claude_settings_env`,
	// `mcp_servers`) are carried inside this key, so a nested path
	// (`agents.bot.acp.claude_settings_env`) resolves here through the longest-prefix
	// rule and a hot edit applies to the next job — every member is read where the job
	// is built (acpRequest) or spawned (the runner), nothing derives a cached value
	// from it. There is deliberately no row per member: EditableAgentFields() doubles
	// as the flat field vocabulary applyAgentWrite CLEARS when a body omits it, and a
	// dotted name there would be cleared as if it were a top-level field.
	"agents.*.acp":                   {Editable: true},
	"agents.*.output_format":         {Editable: true},
	"agents.*.ndjson_keep":           {Editable: true},
	"agents.*.ndjson_raw":            {Editable: true},
	"agents.*.ndjson_events_to":      {Editable: true},
	"agents.*.ndjson_stdout":         {Editable: true},
	"agents.*.ndjson_stdout_path":    {Editable: true},
	"agents.*.ndjson_usage_path":     {Editable: true},
	"agents.*.transcript_dialect":    {Editable: true},
	"agents.*.inject_process":        {Editable: true},
	"agents.*.session_family":        {Editable: true},
	"agents.*.deliver_command":       {Editable: true},
	"agents.*.deliver_stdin":         {Editable: true},
	"agents.*.deliver_offline_match": {Editable: true},
	"agents.*.ndjson_fields":         {Editable: true},
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
// longest known prefix (`agents.*.acp`). Caller ids are a wildcard the same way
// (`server.callers.ci.ceiling` → `server.callers.*.ceiling`), because a caller entry is
// an operator-chosen key like an agent name.
func FieldPolicyFor(path string) (FieldPolicy, bool) {
	if fp, ok := fieldPolicies[path]; ok {
		return fp, true
	}
	parts := strings.Split(path, ".")
	if len(parts) < 3 {
		return FieldPolicy{}, false
	}
	switch parts[0] {
	case "agents", "projects":
		for end := len(parts); end >= 3; end-- {
			if fp, ok := fieldPolicies[parts[0]+".*."+strings.Join(parts[2:end], ".")]; ok {
				return fp, true
			}
		}
	case "server":
		// server.callers.<id>.<field> — the id is the wildcard segment.
		if len(parts) >= 4 && parts[1] == "callers" {
			if fp, ok := fieldPolicies["server.callers.*."+strings.Join(parts[3:], ".")]; ok {
				return fp, true
			}
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
