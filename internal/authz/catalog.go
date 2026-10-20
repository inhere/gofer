// Package authz is the permission catalog and the pattern rules that turn a grant
// pattern into the set of permissions the grant means — design §1「权限目录（40 项）」
// and §2「授权模型与判定」of
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md.
//
// The package is leaf logic: it depends on the standard library, on internal/util and on
// internal/config (whose server configuration the §3 derivation reads) — nothing else, and
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

// Label is the label the design table and the generated permission reference use.
func (r Risk) Label() string {
	switch r {
	case RiskLow:
		return "低"
	case RiskMedium:
		return "中"
	case RiskHigh:
		return "高"
	case RiskDangerous:
		return "危险"
	case RiskRoot:
		return "根"
	default:
		return string(r)
	}
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

// Label is the label the design table and the generated permission reference use.
func (s Source) Label() string {
	switch s {
	case SourceSeed:
		return "种入"
	case SourceConfig:
		return "配置"
	case SourceRoot:
		return "仅根"
	default:
		return string(s)
	}
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

// Label is the label the design table and the generated permission reference use.
func (s Scope) Label() string {
	switch s {
	case ScopeNo:
		return "否"
	case ScopeYes:
		return "是"
	case ScopePhase2:
		return "第二期"
	default:
		return string(s)
	}
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
	// Description is what the permission covers (the design table's「覆盖」column): the
	// routes and entry points it stands for.
	Description string
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

// Validate reports whether the entry is internally consistent. It is the invariant the
// catalog test enforces on every row, including the seeding rule: only a seeded row
// carries a seed version.
func (p Permission) Validate() error {
	switch {
	case p.Name == "":
		return errInvalid("empty permission name")
	case strings.ContainsAny(p.Name, " \t*"):
		return errInvalid("permission name %q must be literal (no space, tab or wildcard)", p.Name)
	case p.Description == "":
		return errInvalid("permission %s has no description", p.Name)
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
	PermTrackerSync   = "tracker.sync"
	PermTrackerDelete = "tracker.delete"

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
		Description: "读他人的 job、workflow、workbench 线程、interaction 列表（含 `GET /v1/interactions`）、wakeup / retry、SSE 流；过滤型",
	},
	{
		Name: PermJobSubmit, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "一切产生 job 的入口：jobs、workflows、schedules 创建 / run-now / enable、plan run / resume / PATCH 开启 leader、todo dispatch 与 PATCH（改为 ready 或改 assignee 即派发）、resume / rebuild、`reject?resume=true`、会话 resume、deliver（允许接管时起 pty job）、wakeups 创建与修改、agent probe、评论 @ 派发、workbench agent 线程。这些入口只在「这次请求会产生 job」时另需本权限（InHandler）",
	},
	{
		Name: PermJobCancelOthers, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "取消他人 job、workflow、retry",
	},
	{
		Name: PermJobManageOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "他人 job 的 resume / rebuild / say / end / title / worktree 删除与合并 / deliver / workflow pick / 注入 interaction，以及对**会话 job**（ACP / pty）说话：session-ask / report-request / workbench 线程中目标为会话 job 的（产生 job 的另需 `job.submit`）",
	},
	{
		Name: PermJobDeleteOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
		Description: "删除、脱敏他人 job",
	},
	{
		Name: PermJobAttach, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceConfig,
		Description: "attach 自己的交互 job、看自己的 pty 录像与会话",
	},
	{
		Name: PermJobAttachOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
		Description: "attach 他人 job、他人录像；全局 pty 会话列表（过滤型）",
	},
	{
		Name: PermJobReview, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceConfig,
		Description: "验收、拒绝、批准 hold（只有人签收）",
	},
	{
		Name: PermJobReviewOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV2,
		Description: "审阅（accept / reject / approve）**他人提交的** job；自己的 job 仍只需 `job.review`",
	},
	{
		Name: PermJobWakeOthers, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceConfig,
		Description: "唤醒他人 job",
	},
	{
		Name: PermInteractionAnswer, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceConfig,
		Description: "回答 job 的工具审批与提问、punt、非中继类决策、推送通知按钮",
	},
	{
		Name: PermSessionReadOthers, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "他人会话的列表 / 详情 / 消息 / tail / turn / nudge / 收件箱（过滤型；outbox 不在此列）",
	},
	{
		Name: PermSessionReplyOthers, Risk: RiskHigh, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "向他人**终端会话**说话或读其待发内容：say / messages / ack / deliver / relay / outbox / 中继类决策回答 / workbench 终端会话线程 / 目标为终端会话的 session-ask 与 report-request",
	},
	{
		Name: PermSessionManageOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "删除他人会话、开 / 释放 turn、release-takeover",
	},
	{
		Name: PermSessionPermissionAnswer, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
		Description: "回答**他人**终端的工具权限提示（会话 owner 自己不需要）",
	},
	{
		Name: PermSessionTransfer, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
		Description: "转移会话归属",
	},
	{
		Name: PermPlanManageOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "他人 plan 的修改 / run / pause / resume / attach / todo 增改与派发 / handoff",
	},
	{
		Name: PermPlanSuperviseOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
		Description: "给他人 plan 绑定 supervisor，或绑定他人会话",
	},
	{
		Name: PermScheduleManageOthers, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "他人 schedule 的删除 / 启停 / 轮换 token / run-now（run-now 另需 `job.submit`）",
	},
	{
		Name: PermStewardManage, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "steward start / stop / restart / ask / review，以及 notes 写入",
	},
	{
		Name: PermMemoryWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "写、删 tracker 记忆与 scoped 记忆（会注入所有 job 的 prompt），采纳记忆建议",
	},
	{
		Name: PermTrackerSync, Risk: RiskLow, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "推快照、repo sync",
	},
	{
		Name: PermTrackerDelete, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceConfig,
		Description: "删除 tracker 镜像",
	},
	{
		Name: PermTunnelOpen, Risk: RiskMedium, SessionGrantable: true,
		Scope: ScopePhase2, Source: SourceConfig,
		Description: "开隧道连接",
	},
	{
		Name: PermTunnelHosted, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "托管转发启停、写或删预设、导入预设、autostart（另需 `tunnel.open`）",
	},
	{
		Name: PermTunnelManageOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
		Description: "停他人的转发与托管转发（隧道与转发的列表为 Public，保持现状）",
	},
	{
		Name: PermXferWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "创建传输（在执行主机读写文件）",
	},
	{
		Name: PermXferManageOthers, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
		Description: "读取、下载、删除他人的传输",
	},
	{
		Name: PermSecretScanAll, Risk: RiskHigh, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
		Description: "跨调用方秘密扫描与脱敏（过滤型）",
	},
	{
		Name: PermUpdateCheck, Risk: RiskLow, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "触发检查新版本（`GET /v1/update` 为 Public）",
	},
	{
		Name: PermUpdateApply, Risk: RiskHigh, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "一键更新执行与取消；带 workers 时另需 `worker.upgrade`",
	},
	{
		Name: PermWorkerUpgrade, Risk: RiskHigh, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "用 server 自身二进制升级 worker",
	},
	{
		Name: PermWorkerUpgradeUpload, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "上传二进制；`source=staged` 也需要",
	},
	{
		Name: PermWorkerManage, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "注册、删除、重载 worker",
	},
	{
		Name: PermConfigRead, Risk: RiskLow, SessionGrantable: true,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "`GET /v1/config`、MCP 注册表读取（`/meta`、`/agents`、`/runners` 为 Public）",
	},
	{
		Name: PermMcpTest, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceSeed, SeedVersion: SeedV1,
		Description: "测试 MCP server 连接（会在 server 上起进程）",
	},
	{
		Name: PermSettingsRead, Risk: RiskLow, SessionGrantable: true,
		Scope: ScopeYes, Source: SourceSeed, SeedVersion: SeedV3,
		Description: "读取扩展设置（`GET /v1/settings`、单个键、导出）：键表、默认值、当前值、说明",
	},
	{
		Name: PermSettingsWrite, Risk: RiskMedium, SessionGrantable: false,
		Scope: ScopeYes, Source: SourceRoot,
		Description: "写扩展设置（PUT / DELETE 单个键、导入）：提示词、阈值与排除名单；每次写入记审计",
	},
	{
		Name: PermConfigWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "写配置（含 steward / work）、校验、重载",
	},
	{
		Name: PermProjectWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "项目增删改",
	},
	{
		Name: PermRulesWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "规则库写入",
	},
	{
		Name: PermSkillsWrite, Risk: RiskDangerous, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceConfig,
		Description: "skills 导入、更新、删除",
	},
	{
		Name: PermAuthManage, Risk: RiskRoot, SessionGrantable: false,
		Scope: ScopeNo, Source: SourceRoot,
		Description: "管理授权（仅根管理员，任何授权都匹配不到）",
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
