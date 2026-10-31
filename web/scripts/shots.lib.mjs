// shots.mjs 的纯函数部分：参数解析、组合展开、检查规则对 DOM 采样数据的判定。
//
// 不 import playwright，也不碰 DOM / 网络，所以能直接用 vitest 测（shots.lib.test.mjs）。
// 唯一例外是 collectSample：它会被序列化后在页面里执行（page.evaluate），必须自包含，
// 不能引用本文件的任何其他符号。
//
// 检查项出自设计稿 docs/design/2026-10-23-ui-redesign-design.md §13「截图检查」：
//   1. 没有页面级横向滚动；
//   2. 没有尺寸超过 40px 的行内 SVG；
//   3. 计数、标签没有被断成多行。
// 另加一项 warn 级：英文界面下残留中文（P1 验收口径「zh/en 切换后外壳没有残留中文」）。
// 截图本身只给人看，不做像素比对。

export const DEFAULT_PORT = 8765 // gofer serve 默认端口（vite 开发服务器代理也指向它）
export const DEFAULT_BASE = `http://127.0.0.1:${DEFAULT_PORT}`
export const DEFAULT_WIDTHS = [1440, 1024, 800, 390]
export const THEMES = ['light', 'dark']
// 与 web/src/store/locale.ts 的 Locale 一致；命令行写短名 zh / en。
export const LOCALES = ['zh-CN', 'en-US']
export const DEFAULT_OUT = 'web/shots'
export const DEFAULT_TOKEN_ENV = 'GOFER_TOKEN'
export const MAX_INLINE_SVG_PX = 40

// localStorage 键（读自 web/src/store/{auth,theme,locale}.ts）
export const STORAGE_KEYS = {
  token: 'gofer-token',
  theme: 'gofer.theme',
  locale: 'gofer.locale',
}

// 6 个代表页；有 detail 的再拍一张详情页（id 从列表接口取第一条）。
// path 是 SPA 路由；detail.api 是取 id 的 REST 列表，detail.listKey 是响应里的数组字段。
export const PAGES = [
  { name: 'today', path: '/today' },
  { name: 'workbench', path: '/workbench' },
  { name: 'jobs', path: '/jobs', detail: { api: '/v1/jobs?limit=1', listKey: 'jobs', path: (id) => `/jobs/${encodeURIComponent(id)}` } },
  { name: 'plans', path: '/plans', detail: { api: '/v1/plans?limit=1', listKey: 'plans', path: (id) => `/plans/${encodeURIComponent(id)}` } },
  { name: 'sessions', path: '/sessions' },
  { name: 'workflows', path: '/workflows', detail: { api: '/v1/workflows?limit=1', listKey: 'workflows', path: (id) => `/workflows/${encodeURIComponent(id)}` } },
]

// ---- 参数 ----

export class UsageError extends Error {}

export const USAGE = `Usage: node web/scripts/shots.mjs [options]

  --base <url>         gofer serve 或 vite dev 的地址（默认 ${DEFAULT_BASE}）
  --token-env <NAME>   从环境变量 NAME 读 token（默认 ${DEFAULT_TOKEN_ENV}）；token 不接受命令行明文
  --out <dir>          输出目录（默认 ${DEFAULT_OUT}，即脚本所在 web/ 下的 shots/；给定时相对当前目录；web/.gitignore 已忽略 shots/）
  --pages <a,b>        只拍这些页（${PAGES.map((p) => p.name).join(',')}）
  --widths <n,n>       只拍这些宽度（默认 ${DEFAULT_WIDTHS.join(',')}）
  --themes <light,dark>  只拍这些主题（默认两种）
  --langs <zh,en>      只拍这些语言（默认两种）
  --no-detail          不拍详情页
  --channel <name>     用系统浏览器（如 msedge / chrome）代替 playwright 自带的 chromium
  --settle <ms>        页面加载后额外等待（默认 800）
  --fail-on-warn       warn 级问题（英文下残留中文）也算失败
  --help
`

const VALUE_FLAGS = new Set(['base', 'token-env', 'out', 'pages', 'widths', 'themes', 'langs', 'channel', 'settle'])
const BOOL_FLAGS = new Set(['no-detail', 'fail-on-warn', 'help'])

function splitList(v) {
  return String(v)
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

export function normalizeLang(v) {
  const s = String(v).trim().toLowerCase()
  if (s === 'zh' || s.startsWith('zh-')) return 'zh-CN'
  if (s === 'en' || s.startsWith('en-')) return 'en-US'
  throw new UsageError(`未知语言 ${JSON.stringify(v)}（用 zh / en）`)
}

// parseArgs：argv 不含 node 与脚本名。env 只用来读 token，token 不进返回值的可打印部分之外
// 的任何地方（调用方不要把 opts.token 写日志 / report）。
export function parseArgs(argv, env = {}) {
  const raw = {}
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (!a.startsWith('--')) throw new UsageError(`多余的参数 ${JSON.stringify(a)}`)
    let name = a.slice(2)
    let val
    const eq = name.indexOf('=')
    if (eq >= 0) {
      val = name.slice(eq + 1)
      name = name.slice(0, eq)
    }
    if (name === 'token') {
      throw new UsageError('不接受命令行明文 --token（会进 shell 历史和进程列表）；请把 token 放进环境变量，用 --token-env <NAME> 指定（默认 GOFER_TOKEN）')
    }
    if (BOOL_FLAGS.has(name)) {
      raw[name] = true
      continue
    }
    if (!VALUE_FLAGS.has(name)) throw new UsageError(`未知参数 --${name}`)
    if (val === undefined) {
      val = argv[++i]
      if (val === undefined || val.startsWith('--')) throw new UsageError(`--${name} 需要一个值`)
    }
    raw[name] = val
  }
  if (raw.help) return { help: true }

  let widths = DEFAULT_WIDTHS
  if (raw.widths !== undefined) {
    widths = splitList(raw.widths).map((s) => {
      const n = Number(s)
      if (!Number.isInteger(n) || n < 200 || n > 5000) throw new UsageError(`宽度不合法：${s}`)
      return n
    })
    if (widths.length === 0) throw new UsageError('--widths 为空')
  }
  let pages = PAGES.map((p) => p.name)
  if (raw.pages !== undefined) {
    pages = splitList(raw.pages)
    const known = new Set(PAGES.map((p) => p.name))
    const bad = pages.filter((p) => !known.has(p))
    if (bad.length || pages.length === 0) throw new UsageError(`未知页面：${bad.join(',') || '(空)'}（可选 ${[...known].join(',')}）`)
  }
  let themes = THEMES
  if (raw.themes !== undefined) {
    themes = splitList(raw.themes)
    const bad = themes.filter((x) => !THEMES.includes(x))
    if (bad.length || themes.length === 0) throw new UsageError(`未知主题：${bad.join(',') || '(空)'}（light / dark）`)
  }
  let langs = LOCALES
  if (raw.langs !== undefined) {
    langs = splitList(raw.langs).map(normalizeLang)
    if (langs.length === 0) throw new UsageError('--langs 为空')
  }
  let settleMs = 800
  if (raw.settle !== undefined) {
    settleMs = Number(raw.settle)
    if (!Number.isFinite(settleMs) || settleMs < 0) throw new UsageError(`--settle 不合法：${raw.settle}`)
  }
  const base = String(raw.base ?? DEFAULT_BASE).replace(/\/+$/, '')
  try {
    const u = new URL(base)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new Error('protocol')
  } catch {
    throw new UsageError(`--base 不是 http(s) 地址：${base}`)
  }
  const tokenEnv = String(raw['token-env'] ?? DEFAULT_TOKEN_ENV)
  return {
    help: false,
    base,
    token: env[tokenEnv] ? String(env[tokenEnv]) : '',
    tokenEnv,
    out: String(raw.out ?? DEFAULT_OUT),
    outGiven: raw.out !== undefined,
    pages,
    widths,
    themes,
    langs,
    detail: !raw['no-detail'],
    channel: raw.channel ? String(raw.channel) : '',
    settleMs,
    failOnWarn: !!raw['fail-on-warn'],
  }
}

// ---- 组合展开 ----

// viewportFor：窄屏用手机高度，其余桌面高度（fullPage 截图，高度只影响首屏与 100vh 布局）。
export function viewportFor(width) {
  return { width, height: width <= 760 ? 844 : 900 }
}

// expandCombos：按「宽度 × 主题 × 语言」分组（同组共用一个浏览器上下文），组内是页面列表。
// 详情页（detailIds 里有 id）紧跟其列表页。顺序固定，便于对比 report。
// detailIds: { [pageName]: id }，缺的页面不产生详情条目。
export function expandCombos({ pages, widths, themes, langs, detailIds = {}, includeDetail = true, catalog = PAGES }) {
  const wanted = new Set(pages)
  const targets = []
  for (const p of catalog) {
    if (!wanted.has(p.name)) continue
    targets.push({ page: p.name, kind: 'list', path: p.path })
    const id = detailIds[p.name]
    if (includeDetail && p.detail && id) {
      targets.push({ page: p.name, kind: 'detail', path: p.detail.path(id) })
    }
  }
  const groups = []
  for (const width of widths) {
    for (const theme of themes) {
      for (const lang of langs) {
        groups.push({ width, theme, lang, targets })
      }
    }
  }
  return groups
}

export function countShots(groups) {
  return groups.reduce((n, g) => n + g.targets.length, 0)
}

export function shotName(g, target) {
  const lang = g.lang === 'zh-CN' ? 'zh' : 'en'
  const kind = target.kind === 'detail' ? '-detail' : ''
  return `${target.page}${kind}__${g.width}__${g.theme}__${lang}.png`
}

// 把详情 id 从列表接口响应里取出来。
export function pickDetailId(resp, listKey) {
  const arr = resp && Array.isArray(resp[listKey]) ? resp[listKey] : []
  const first = arr[0]
  const id = first && (first.id ?? first.ID)
  return typeof id === 'string' && id ? id : ''
}

// ---- 页面内采样（序列化后在浏览器里执行，必须自包含） ----

export function collectSample(opts) {
  const CJK = /[㐀-䶿一-鿿　-〿＀-￯]/
  const visible = (el) => {
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) return false
    const cs = getComputedStyle(el)
    return cs.visibility !== 'hidden' && cs.display !== 'none' && cs.opacity !== '0'
  }
  const describe = (el) => {
    let s = el.tagName.toLowerCase()
    const tid = el.getAttribute('data-testid') || el.getAttribute('data-test')
    if (tid) s += `[${tid}]`
    else if (typeof el.className === 'string' && el.className.trim()) s += '.' + el.className.trim().split(/\s+/).slice(0, 3).join('.')
    return s
  }
  const doc = document.documentElement
  const sample = {
    url: location.pathname + location.search,
    viewportWidth: window.innerWidth,
    scrollWidth: Math.max(doc.scrollWidth, document.body ? document.body.scrollWidth : 0),
    clientWidth: doc.clientWidth,
    svgs: [],
    wraps: [],
    cjk: [],
  }

  for (const svg of document.querySelectorAll('svg')) {
    if (svg.closest('[data-shots-allow-svg]')) continue
    if (!visible(svg)) continue
    const r = svg.getBoundingClientRect()
    sample.svgs.push({ el: describe(svg), width: Math.round(r.width), height: Math.round(r.height) })
  }

  // 计数 / 标签：徽标、chip、pill、tag、标签栏文字、导航文字、tab。
  const selector = [
    '.pill', '.chip', '.tag', '.badge', '.kbd',
    '.side-count', '.side-label', '.tab-label', '.more-label', '.new-job-label', '.conn',
    '[class*="count"]', '[class*="badge"]', '[role="tab"]',
  ].join(',')
  const seen = new Set()
  for (const el of document.querySelectorAll(selector)) {
    if (seen.has(el) || !visible(el)) continue
    seen.add(el)
    const text = (el.textContent || '').trim()
    if (!text) continue
    const cs = getComputedStyle(el)
    // 只看会换行的元素：nowrap / 明确截断的不算
    if (cs.whiteSpace === 'nowrap' || cs.whiteSpace === 'pre') continue
    const r = el.getBoundingClientRect()
    let lh = parseFloat(cs.lineHeight)
    if (!Number.isFinite(lh)) lh = parseFloat(cs.fontSize) * 1.3
    const pad = (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0) + (parseFloat(cs.borderTopWidth) || 0) + (parseFloat(cs.borderBottomWidth) || 0)
    const lines = Math.round((r.height - pad) / lh)
    sample.wraps.push({ el: describe(el), text: text.slice(0, 40), lines, height: Math.round(r.height), lineHeight: Math.round(lh) })
  }

  if (opts && opts.lang === 'en-US') {
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    let n
    while ((n = walker.nextNode())) {
      const v = n.nodeValue || ''
      if (!CJK.test(v)) continue
      const p = n.parentElement
      if (!p || /^(SCRIPT|STYLE|NOSCRIPT)$/.test(p.tagName) || !visible(p)) continue
      if (p.closest('[data-shots-allow-cjk]')) continue
      sample.cjk.push({ el: describe(p), text: v.trim().slice(0, 40) })
    }
    // 属性里的文案（title / aria-label / placeholder）同样算
    for (const el of document.querySelectorAll('[title],[aria-label],[placeholder]')) {
      if (!visible(el) || el.closest('[data-shots-allow-cjk]')) continue
      for (const a of ['title', 'aria-label', 'placeholder']) {
        const v = el.getAttribute(a)
        if (v && CJK.test(v)) sample.cjk.push({ el: describe(el), text: `${a}=${v.slice(0, 36)}` })
      }
    }
  }
  return sample
}

// ---- 检查规则（纯判定） ----

const MAX_PER_CHECK = 10

// evaluateSample：返回 [{check, severity, message, detail?}]。severity: error | warn。
// sample 缺字段按空处理，便于单测。
export function evaluateSample(sample, { lang = 'zh-CN' } = {}) {
  const issues = []
  const s = sample ?? {}

  // 1. 页面级横向滚动：容差 1px（亚像素取整）。
  const sw = Number(s.scrollWidth) || 0
  const cw = Number(s.clientWidth) || Number(s.viewportWidth) || 0
  if (cw > 0 && sw > cw + 1) {
    issues.push({
      check: 'horizontal-overflow',
      severity: 'error',
      message: `页面横向溢出：scrollWidth ${sw} > clientWidth ${cw}`,
    })
  }

  // 2. 行内 SVG 超过 40px。
  const bigSvgs = (s.svgs ?? []).filter((v) => Math.max(v.width, v.height) > MAX_INLINE_SVG_PX)
  for (const v of bigSvgs.slice(0, MAX_PER_CHECK)) {
    issues.push({
      check: 'oversize-svg',
      severity: 'error',
      message: `行内 SVG ${v.width}x${v.height} 超过 ${MAX_INLINE_SVG_PX}px：${v.el}`,
    })
  }
  if (bigSvgs.length > MAX_PER_CHECK) {
    issues.push({ check: 'oversize-svg', severity: 'error', message: `……另有 ${bigSvgs.length - MAX_PER_CHECK} 个超限 SVG` })
  }

  // 3. 计数 / 标签被断成多行。
  const wrapped = (s.wraps ?? []).filter((v) => v.lines >= 2)
  for (const v of wrapped.slice(0, MAX_PER_CHECK)) {
    issues.push({
      check: 'wrapped-label',
      severity: 'error',
      message: `标签断成 ${v.lines} 行：${v.el} 「${v.text}」`,
    })
  }
  if (wrapped.length > MAX_PER_CHECK) {
    issues.push({ check: 'wrapped-label', severity: 'error', message: `……另有 ${wrapped.length - MAX_PER_CHECK} 处断行` })
  }

  // 4. 英文界面残留中文（warn：用户数据本身可能含中文）。
  if (lang === 'en-US') {
    const cjk = s.cjk ?? []
    for (const v of cjk.slice(0, MAX_PER_CHECK)) {
      issues.push({ check: 'residual-cjk', severity: 'warn', message: `英文界面出现中文：${v.el} 「${v.text}」` })
    }
    if (cjk.length > MAX_PER_CHECK) {
      issues.push({ check: 'residual-cjk', severity: 'warn', message: `……另有 ${cjk.length - MAX_PER_CHECK} 处` })
    }
  }
  return issues
}

// 页面被送去登录页 / 打不开也算一条错误，由 runner 直接构造。
export function navigationIssue(expectedPath, finalPath) {
  const strip = (p) => p.split('?')[0].replace(/\/+$/, '') || '/'
  if (strip(finalPath) === strip(expectedPath)) return null
  if (strip(finalPath) === '/access') {
    return { check: 'navigation', severity: 'error', message: `被重定向到 /access：token 缺失或无效（期望 ${expectedPath}）` }
  }
  return { check: 'navigation', severity: 'error', message: `落在 ${finalPath}，期望 ${expectedPath}` }
}

// ---- 汇总 ----

// entries: [{page, kind, width, theme, lang, file, issues:[...]}]
export function summarize(entries) {
  const byCheck = {}
  let errors = 0
  let warnings = 0
  let failedShots = 0
  for (const e of entries) {
    let hasError = false
    for (const i of e.issues ?? []) {
      byCheck[i.check] = byCheck[i.check] ?? { error: 0, warn: 0 }
      byCheck[i.check][i.severity] += 1
      if (i.severity === 'error') {
        errors++
        hasError = true
      } else {
        warnings++
      }
    }
    if (hasError) failedShots++
  }
  return { shots: entries.length, errors, warnings, failedShots, byCheck }
}

export function exitCode(summary, { failOnWarn = false } = {}) {
  if (summary.errors > 0) return 1
  if (failOnWarn && summary.warnings > 0) return 1
  return 0
}
