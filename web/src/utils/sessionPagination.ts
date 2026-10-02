export function mergeOlderPage<T extends { id?: string } | string>(current: T[], older: T[]): T[] {
  const seen = new Set(current.map((item) => typeof item === 'string' ? item : item.id ?? ''))
  return [...current, ...older.filter((item) => {
    const id = typeof item === 'string' ? item : item.id ?? ''
    if (seen.has(id)) return false
    seen.add(id)
    return true
  })]
}

export function mergeNewestPage<T extends { id?: string } | string>(newest: T[], current: T[]): T[] {
  const seen = new Set<string>()
  const out: T[] = []
  for (const item of [...newest, ...current]) {
    const id = typeof item === 'string' ? item : item.id ?? ''
    if (seen.has(id)) continue
    seen.add(id)
    out.push(item)
  }
  return out
}

export function preserveScrollAfterPrepend(input: { beforeHeight: number; beforeTop: number; afterHeight: number }): number {
  return input.beforeTop + Math.max(0, input.afterHeight - input.beforeHeight)
}

export function shouldFollowBottom(input: { scrollHeight: number; scrollTop: number; clientHeight: number; threshold?: number }): boolean {
  return input.scrollHeight - input.scrollTop - input.clientHeight <= (input.threshold ?? 24)
}
