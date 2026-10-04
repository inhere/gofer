// 本机内置 runner 的面向用户显示名（G043）。
// 内部/wire/DB 规范名恒为 `local`，CLI 对外拼写是 `server`；web 上所有给人看的地方统一显示 `server`。
// 只改显示：提交给后端、做比较、做 key 的值一律保持原样（后端两种拼写都接受）。
//
// 例外（declare-wins）：operator 在 `runners:` 里真声明了一个叫 `server` 的非本机 runner 时，
// 内置本机 runner 若也显示成 server 会重名，此时退回显示规范名 `local`。

export const LOCAL_RUNNER = 'local'
export const LOCAL_RUNNER_DISPLAY = 'server'

interface NamedRunner {
  name: string
  type?: string
}

// runnerLabel：runner 名 → 显示名。known 可传已知 runner 列表用于 declare-wins 判断。
export function runnerLabel(name: string | undefined | null, known?: NamedRunner[]): string {
  if (!name) return ''
  if (name !== LOCAL_RUNNER) return name
  if (known?.some((r) => r.name === LOCAL_RUNNER_DISPLAY && r.type !== 'local')) return name
  return LOCAL_RUNNER_DISPLAY
}

// runnerLabelNote：需要强调"这是本机"的地方（Runners 卡片 / 分组标题）：`server（本机）`。
export function runnerLabelNote(name: string | undefined | null, known?: NamedRunner[]): string {
  const label = runnerLabel(name, known)
  return label === LOCAL_RUNNER_DISPLAY && name === LOCAL_RUNNER ? `${label}（本机）` : label
}

interface OptionRunner extends NamedRunner {
  type: string
  worker_id?: string
}

// runnerOptionText：runner 下拉项文案。本机显示 `server · 本机`（而不是 `local · local`）。
export function runnerOptionText(r: OptionRunner, known?: NamedRunner[]): string {
  const base = r.type === 'local' ? `${runnerLabel(r.name, known)} · 本机` : `${r.name} · ${r.type}`
  return r.worker_id ? `${base} · ${r.worker_id}` : base
}

// isLocalRunnerName：是否本机 runner（兼容两种拼写；只用于显示/灰显判断，不改提交值）。
export function isLocalRunnerName(name: string | undefined | null): boolean {
  return name === LOCAL_RUNNER || name === LOCAL_RUNNER_DISPLAY || name === '' || name == null
}
