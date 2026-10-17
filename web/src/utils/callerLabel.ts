// 调用方显示名（callers[].name，可选）：只改显示，id 仍是身份（任务 / 授权 / 审计里记的都是 id）。
// names 是服务端 whoami / 授权列表返回的 caller_names（id → 名称，仅含配了名称的调用方）。

export type CallerNames = Readonly<Record<string, string>> | null | undefined

// callerLabel：有名称显示名称，否则显示 id。
export function callerLabel(id: string | null | undefined, names?: CallerNames): string {
  const cid = id ?? ''
  if (!cid) return ''
  return names?.[cid]?.trim() || cid
}

// callerTitle：悬停 / 详情用，名称与 id 同时可见；没有名称时就是 id。
export function callerTitle(id: string | null | undefined, names?: CallerNames): string {
  const cid = id ?? ''
  if (!cid) return ''
  const name = names?.[cid]?.trim()
  return name && name !== cid ? `${name} (${cid})` : cid
}
