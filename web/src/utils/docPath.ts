import type { RouteLocationRaw } from 'vue-router'

export interface DocRef {
  path: string
  line?: number
  anchor?: string
}

export interface DocContext {
  projectKey: string
  base?: string
  worktreeBranch?: string
  path?: string
}

export function parseDocRef(raw: string): DocRef | null {
  const value = raw.trim().replace(/^`+|`+$/g, '')
  if (!value || value.includes('://') || value.includes('\\') || value.startsWith('/') || value.startsWith('~')) {
    return null
  }
  const hash = value.indexOf('#')
  const beforeAnchor = hash >= 0 ? value.slice(0, hash) : value
  const anchor = hash >= 0 ? value.slice(hash + 1) : undefined
  const lineMatch = beforeAnchor.match(/:(\d+)$/)
  const path = (lineMatch ? beforeAnchor.slice(0, -lineMatch[0].length) : beforeAnchor)
  if (!path || (anchor !== undefined && !anchor)) return null
  return { path, ...(lineMatch ? { line: Number(lineMatch[1]) } : {}), ...(anchor ? { anchor } : {}) }
}

export function isDocPath(path: string): boolean {
  const ref = parseDocRef(path)
  if (!ref || /\s/.test(path) || ref.path.includes(':')) return false
  const parts = ref.path.split('/')
  if (parts.some((part) => !part || part === '..' || part.startsWith('.'))) return false
  return /\.(?:md|markdown)$/i.test(ref.path)
}

export function findDocPaths(text: string): Array<DocRef & { start: number; end: number }> {
  const out: Array<DocRef & { start: number; end: number }> = []
  const tokenRe = /(?<![A-Za-z0-9_./~:<>-])(?:[A-Za-z0-9_-]+\/)*[A-Za-z0-9_.-]+\.(?:md|markdown)(?::\d+|#[^\s`"'()[\]{}<>，。；：！？、]*)?/gi
  for (const match of text.matchAll(tokenRe)) {
    const token = match[0]
    const ref = parseDocRef(token)
    if (!ref || !isDocPath(token)) continue
    const start = match.index ?? 0
    out.push({ ...ref, start, end: start + token.length })
  }
  return out
}

export function resolveDocHref(fromDir: string, href: string): string | null {
  if (!href || href.startsWith('#') || href.includes('://') || href.startsWith('/') || href.startsWith('~')) return null
  const ref = parseDocRef(href)
  if (!ref || /\s/.test(href) || ref.path.includes(':') || ref.path.split('/').some((part) => part.startsWith('.') && part !== '..') || !/\.(?:md|markdown)$/i.test(ref.path)) return null
  const segments = [...(fromDir ? fromDir.split('/') : []), ...ref.path.split('/')]
  const resolved: string[] = []
  for (const segment of segments) {
    if (!segment || segment === '.') continue
    if (segment === '..') {
      if (!resolved.length) return null
      resolved.pop()
    } else {
      resolved.push(segment)
    }
  }
  const path = resolved.join('/')
  if (!isDocPath(path)) return null
  return `${path}${ref.anchor ? `#${ref.anchor}` : ''}${ref.line !== undefined ? `:${ref.line}` : ''}`
}

export function docReaderLocation(ctx: DocContext, ref: DocRef | string): RouteLocationRaw {
  const parsed = typeof ref === 'string' ? parseDocRef(ref) : ref
  if (!parsed) return { path: `/projects/${encodeURIComponent(ctx.projectKey)}/doc` }
  const query: Record<string, string> = { path: parsed.path }
  if (ctx.base && ctx.base !== '.') query.base = ctx.base
  if (parsed.line !== undefined) query.line = String(parsed.line)
  if (parsed.anchor) query.anchor = parsed.anchor
  if (ctx.worktreeBranch) query.wtb = ctx.worktreeBranch
  return { path: `/projects/${encodeURIComponent(ctx.projectKey)}/doc`, query }
}
