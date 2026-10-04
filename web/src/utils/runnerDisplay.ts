// 本机内置 runner 的面向用户显示名（G043）。
// 内部/wire/DB 规范名恒为 `local`，CLI 对外拼写是 `server`；web 上所有给人看的地方统一显示 `server`。
// 只改显示：提交给后端、做比较、做 key 的值一律保持原样（后端两种拼写都接受）。
// `server` / `local` 是保留名，自定义 runner 不可能叫这两个名字，所以显示映射不需要任何例外。

export const LOCAL_RUNNER = 'local'
export const LOCAL_RUNNER_DISPLAY = 'server'

// runnerLabel：runner 名 → 显示名。
export function runnerLabel(name: string | undefined | null): string {
  if (!name) return ''
  if (name !== LOCAL_RUNNER) return name
  return LOCAL_RUNNER_DISPLAY
}

// runnerLabelNote：需要强调"这是本机"的地方（Runners 卡片 / 分组标题）：`server（本机）`。
export function runnerLabelNote(name: string | undefined | null): string {
  const label = runnerLabel(name)
  return label === LOCAL_RUNNER_DISPLAY && name === LOCAL_RUNNER ? `${label}（本机）` : label
}

interface OptionRunner {
  name: string
  type: string
  worker_id?: string
}

// runnerOptionText：runner 下拉项文案。本机显示 `server · 本机`（而不是 `local · local`）。
export function runnerOptionText(r: OptionRunner): string {
  const base = r.type === 'local' ? `${runnerLabel(r.name)} · 本机` : `${r.name} · ${r.type}`
  return r.worker_id ? `${base} · ${r.worker_id}` : base
}

// isLocalRunnerName：是否本机 runner（兼容两种拼写；只用于显示/灰显判断，不改提交值）。
export function isLocalRunnerName(name: string | undefined | null): boolean {
  return name === LOCAL_RUNNER || name === LOCAL_RUNNER_DISPLAY || name === '' || name == null
}
