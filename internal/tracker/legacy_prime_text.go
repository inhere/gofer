package tracker

import "github.com/inhere/gofer/internal/i18n"

const PrimeMaxBytes = 8 << 10

// Deprecated compatibility values use the default language. New rendering paths pass
// language explicitly through PrimeOptions / scoped options.
var (
	WorkPrimeHint     = i18n.T(i18n.Default, "prime.header.work_hint")
	TrackerPrimeHint  = i18n.T(i18n.Default, "prime.header.tracker_hint") + "\n" + MemoryWriteHint
	PrimeTakeoverHint = i18n.T(i18n.Default, "prime.header.takeover")
)

const MemoryWriteHint = "写记忆：长期约定用 `--kind rule`（写现状不写进度）；阶段进度写 plan 交接说明或 `--kind handoff`（默认 14 天后过期）；正文超 200 字要 `--summary`。"
