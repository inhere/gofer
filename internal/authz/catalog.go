// Package authz is the permission catalog and the pattern rules that turn a grant
// pattern into the set of permissions the grant means — design §1「权限目录（40 项）」
// and §2「授权模型与判定」of
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md.
//
// The package is leaf logic: it depends on the standard library, on internal/util and on
// internal/config (whose server configuration the §3 derivation reads) and internal/i18n
// (the text catalog behind the labels and descriptions) — nothing else, and
// no entry layer, job layer or storage ever grows into it. That is what lets the httpapi
// entry layer, the job layer (which receives a decision instead of reading caller config)
// and the offline documentation generator all use it without an import cycle (G022).
//
// A pattern grant is never decided with Match alone: Match is the raw glob. Expand is
// the entry point for grants, because it also applies the safety rules of design §2 —
// no pattern ever reaches auth.manage, a wildcard never reaches a dangerous permission
// (dangerous permissions must be spelled out literally), and a session grant only
// reaches permissions that may be granted to a session.
package authz

import (
	"fmt"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
)

// Risk grades what a permission lets its holder do if it ends up in the wrong hands
// (design §1). 危险 (dangerous) means the holder can execute arbitrary code on the
// server or a worker, or read the root administrator's token; 根 (root) is reserved for
// auth.manage, which no grant can ever match.
type Risk string

const (
	RiskLow       Risk = "low"       // 低
	RiskMedium    Risk = "medium"    // 中
	RiskHigh      Risk = "high"      // 高
	RiskDangerous Risk = "dangerous" // 危险
	RiskRoot      Risk = "root"      // 根
)

// Label is the label the design table and the generated permission reference use: the
// zh-CN text, the language of that document. Shown text follows a language: LabelIn.
func (r Risk) Label() string { return r.LabelIn(i18n.ZhCN) }

// LabelIn is the label in lang (the catalog's authz.risk.*); a value outside the
// catalog is returned as it is.
func (r Risk) LabelIn(lang i18n.Lang) string {
	if !r.Valid() {
		return string(r)
	}
	return i18n.T(lang, "authz.risk."+string(r))
}

// Valid reports whether r is one of the levels the catalog may use.
func (r Risk) Valid() bool {
	switch r {
	case RiskLow, RiskMedium, RiskHigh, RiskDangerous, RiskRoot:
		return true
	default:
		return false
	}
}

// Source says where a permission comes from (design §1「来源」).
type Source string

const (
	// SourceSeed: seeded to `users` once, so that an upgrade keeps today's behaviour
	// (design §3「数据库种入」).
	SourceSeed Source = "seed"
	// SourceConfig: derived from the old capability bits / governance switches on every
	// config load, read-only in the UI (design §3「配置推出」).
	SourceConfig Source = "config"
	// SourceRoot: only the root administrator has it, independent of any switch.
	SourceRoot Source = "root"
)

// Label is the label the design table and the generated permission reference use: the
// zh-CN text, the language of that document. Shown text follows a language: LabelIn.
func (s Source) Label() string { return s.LabelIn(i18n.ZhCN) }

// LabelIn is the label in lang (the catalog's authz.source.*); a value outside the
// catalog is returned as it is.
func (s Source) LabelIn(lang i18n.Lang) string {
	if !s.Valid() {
		return string(s)
	}
	return i18n.T(lang, "authz.source."+string(s))
}

// Valid reports whether s is one of the sources the catalog may use.
func (s Source) Valid() bool {
	switch s {
	case SourceSeed, SourceConfig, SourceRoot:
		return true
	default:
		return false
	}
}

// Scope answers the design table's「范围」column: may this permission be limited to a
// scope (a project, an agent, …)? Phase 1 stores `*` on every grant (design decision
// 10); the column records what the model will allow in phase 2.
type Scope string

const (
	ScopeNo     Scope = "no"     // 否
	ScopeYes    Scope = "yes"    // 是
	ScopePhase2 Scope = "phase2" // 第二期
)

// Label is the label the design table and the generated permission reference use: the
// zh-CN text, the language of that document. Shown text follows a language: LabelIn.
func (s Scope) Label() string { return s.LabelIn(i18n.ZhCN) }

// LabelIn is the label in lang (the catalog's authz.scope.*); a value outside the
// catalog is returned as it is.
func (s Scope) LabelIn(lang i18n.Lang) string {
	if !s.Valid() {
		return string(s)
	}
	return i18n.T(lang, "authz.scope."+string(s))
}

// Valid reports whether s is one of the scopes the catalog may use.
func (s Scope) Valid() bool {
	switch s {
	case ScopeNo, ScopeYes, ScopePhase2:
		return true
	default:
		return false
	}
}

// Seed versions. A permission's SeedVersion is the migration that plants it for the
// `users` subject; it is NotSeeded when the permission is never seeded (it comes from
// the config derivation or belongs to the root administrator alone).
const (
	NotSeeded = 0
	// SeedV1 is the first seed (design §3): the group that has no check at all today.
	SeedV1 = 1
	// SeedV2 is the second seed: a permission that was added to the catalog later and that
	// has to keep an upgrade behaviour-compatible, so it is planted for `users` on top of
	// SeedV1 (design §3「种入只做一次」).
	SeedV2 = 2
	// SeedV3 is the third seed: the extension-settings read permission
	// (docs/design/2026-10-20-work-noise-and-db-settings-design.md). Reading the settings is
	// the default state of the feature — `users` sees the keys and their values — while
	// writing them stays unseeded.
	SeedV3 = 3
)

// Permission is one entry of the catalog. The fields mirror the columns of the design
// table (§1) one for one.
type Permission struct {
	// Name is the fixed permission name, `<resource>.<action>[.others]`. A published
	// name never changes (design decision 1).
	Name string
	// Risk grades the damage the permission can do.
	Risk Risk
	// SessionGrantable reports whether the permission may be granted to a session
	// subject. Dangerous permissions never may.
	SessionGrantable bool
	// Scope records whether the permission can be limited to a scope.
	Scope Scope
	// Source records where the permission comes from.
	Source Source
	// SeedVersion is the seed migration that plants the permission for `users`, or
	// NotSeeded.
	SeedVersion int
}

// errInvalid builds the error Validate returns. Every message names the offending
// permission, so a failing catalog test points straight at the broken row.
func errInvalid(format string, args ...any) error {
	return fmt.Errorf("authz: invalid catalog entry: "+format, args...)
}

// DescriptionKey is the catalog key of the permission's description (authz.perm.*): the
// name with its dots written as underscores, since an INI key cannot carry the section's dots.
func (p Permission) DescriptionKey() string {
	return "authz.perm." + strings.ReplaceAll(p.Name, ".", "_")
}

// DescriptionIn is what the permission covers (the design table's「覆盖」column): the routes
// and entry points it stands for, in lang. The generated reference uses i18n.ZhCN.
func (p Permission) DescriptionIn(lang i18n.Lang) string { return i18n.T(lang, p.DescriptionKey()) }

// hasDescription reports whether every supported language describes the permission.
func hasDescription(name string) bool {
	key := Permission{Name: name}.DescriptionKey()
	for _, l := range i18n.Supported() {
		if !i18n.HasKey(l, key) {
			return false
		}
	}
	return true
}

// Validate reports whether the entry is internally consistent. It is the invariant the
// catalog test enforces on every row, including the seeding rule: only a seeded row
// carries a seed version.
func (p Permission) Validate() error {
	switch {
	case p.Name == "":
		return errInvalid("empty permission name")
	case strings.ContainsAny(p.Name, " \t*"):
		return errInvalid("permission name %q must be literal (no space, tab or wildcard)", p.Name)
	case !hasDescription(p.Name):
		return errInvalid("permission %s has no description in every language", p.Name)
	case !p.Risk.Valid():
		return errInvalid("permission %s has unknown risk %q", p.Name, string(p.Risk))
	case !p.Scope.Valid():
		return errInvalid("permission %s has unknown scope %q", p.Name, string(p.Scope))
	case !p.Source.Valid():
		return errInvalid("permission %s has unknown source %q", p.Name, string(p.Source))
	case p.Source == SourceSeed && p.SeedVersion <= NotSeeded:
		return errInvalid("seeded permission %s has no seed version", p.Name)
	case p.Source != SourceSeed && p.SeedVersion != NotSeeded:
		return errInvalid("permission %s is not seeded but carries seed version %d", p.Name, p.SeedVersion)
	}
	return nil
}

// Permission names, grouped by resource. A published name never changes (design
// decision 1), so handlers and route declarations spell them through these constants
// rather than repeating string literals.
//
// `pty.recordings` is not in the catalog: the design folds pty recordings into
// job.attach / job.attach.others and keeps that row as an explanatory one only.
const (
	// jobs, workflows, workbench threads.
	PermJobReadOthers     = "job.read.others"
	PermJobSubmit         = "job.submit"
	PermJobCancelOthers   = "job.cancel.others"
	PermJobManageOthers   = "job.manage.others"
	PermJobDeleteOthers   = "job.delete.others"
	PermJobAttach         = "job.attach"
	PermJobAttachOthers   = "job.attach.others"
	PermJobReview         = "job.review"
	PermJobReviewOthers   = "job.review.others"
	PermJobWakeOthers     = "job.wake.others"
	PermInteractionAnswer = "interaction.answer"

	// repository writes made on the caller's behalf (a workflow's gofer/push step).
	PermRepoPush = "repo.push"

	// terminal sessions.
	PermSessionReadOthers       = "session.read.others"
	PermSessionReplyOthers      = "session.reply.others"
	PermSessionManageOthers     = "session.manage.others"
	PermSessionPermissionAnswer = "session.permission.answer"
	PermSessionTransfer         = "session.transfer"

	// plans and their todos, schedules.
	PermPlanManageOthers     = "plan.manage.others"
	PermPlanSuperviseOthers  = "plan.supervise.others"
	PermScheduleManageOthers = "schedule.manage.others"

	// steward, memories, tracker.
	PermStewardManage = "steward.manage"
	PermMemoryWrite   = "memory.write"
	// PermMemorySynthesisRun runs a daily memory synthesis (a model call that costs money).
	PermMemorySynthesisRun = "memory.synthesis.run"
	PermTrackerSync        = "tracker.sync"
	PermTrackerDelete      = "tracker.delete"

	// tunnels and file transfers.
	PermTunnelOpen         = "tunnel.open"
	PermTunnelHosted       = "tunnel.hosted"
	PermTunnelManageOthers = "tunnel.manage.others"
	PermXferWrite          = "xfer.write"
	PermXferManageOthers   = "xfer.manage.others"
	PermSecretScanAll      = "secret.scan.all"

	// update, workers.
	PermUpdateCheck         = "update.check"
	PermUpdateApply         = "update.apply"
	PermWorkerUpgrade       = "worker.upgrade"
	PermWorkerUpgradeUpload = "worker.upgrade.upload"
	PermWorkerManage        = "worker.manage"

	// configuration and the assets it carries.
	PermConfigRead    = "config.read"
	PermMcpTest       = "mcp.test"
	PermSettingsRead  = "settings.read"
	PermSettingsWrite = "settings.write"
	PermTriggerRead   = "trigger.read"
	PermConfigWrite   = "config.write"
	PermProjectWrite  = "project.write"
	PermRulesWrite    = "rules.write"
	PermSkillsWrite   = "skills.write"

	// grants themselves.
	PermAuthManage = "auth.manage"
)

// catalog is the frozen permission catalog: one row per row of the design table (§1),
// in the order of that table, minus the explanatory `pty.recordings` row. The catalog
// test pins every row against the design, so an edit here without a matching design
// change fails.
var catalog = []Permission{
	{
		Name: PermJobReadOthers, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermJobSubmit, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermJobCancelOthers, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermJobManageOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermJobDeleteOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
	},
	{
		Name: PermJobAttach, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceConfig,
	},
	{
		Name: PermJobAttachOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
	},
	{
		Name: PermJobReview, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceConfig,
	},
	{
		Name: PermRepoPush, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
	},
	{
		Name: PermJobReviewOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV2,
	},
	{
		Name: PermJobWakeOthers, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceConfig,
	},
	{
		Name: PermInteractionAnswer, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceConfig,
	},
	{
		Name: PermSessionReadOthers, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermSessionReplyOthers, Risk: RiskHigh, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermSessionManageOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermSessionPermissionAnswer, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
	},
	{
		Name: PermSessionTransfer, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
	},
	{
		Name: PermPlanManageOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermPlanSuperviseOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
	},
	{
		Name: PermScheduleManageOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermStewardManage, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermMemoryWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermMemorySynthesisRun, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
	},
	{
		Name: PermTrackerSync, Risk: RiskLow, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermTrackerDelete, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceConfig,
	},
	{
		Name: PermTunnelOpen, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopePhase2, Source: SourceConfig,
	},
	{
		Name: PermTunnelHosted, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermTunnelManageOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
	},
	{
		Name: PermXferWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermXferManageOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
	},
	{
		Name: PermSecretScanAll, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
	},
	{
		Name: PermUpdateCheck, Risk: RiskLow, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermUpdateApply, Risk: RiskHigh, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermWorkerUpgrade, Risk: RiskHigh, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermWorkerUpgradeUpload, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermWorkerManage, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermConfigRead, Risk: RiskLow, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermMcpTest, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
	},
	{
		Name: PermSettingsRead, Risk: RiskLow, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV3,
	},
	{
		Name: PermSettingsWrite, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
	},
	{
		Name: PermTriggerRead, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
	},
	{
		Name: PermConfigWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermProjectWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermRulesWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermSkillsWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
	},
	{
		Name: PermAuthManage, Risk: RiskRoot, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
	},
}

// Permissions returns the catalog in the order of the design table (§1). The result is
// a copy: callers may sort, filter or otherwise keep it.
func Permissions() []Permission {
	return slices.Clone(catalog)
}

// Lookup returns the catalog entry for name, and false if name is not a permission.
func Lookup(name string) (Permission, bool) {
	for _, p := range catalog {
		if p.Name == name {
			return p, true
		}
	}
	return Permission{}, false
}

// Seeded returns the permissions seed version v plants for the `users` subject, in
// design order. SeedV1 is the 16 permissions of design §3; SeedV2 is the 审阅 batch of
// docs/design/2026-10-18-caller-scope-ceilings-design.md (`job.review.others`). When a
// later catalog addition has to keep an upgrade behaviour-compatible, the version is
// bumped and only the new rows are seeded (design §3「种入只做一次」).
func Seeded(v int) []Permission {
	if v <= NotSeeded {
		return nil
	}
	var out []Permission
	for _, p := range catalog {
		if p.SeedVersion == v {
			out = append(out, p)
		}
	}
	return out
}
