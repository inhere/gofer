// 外壳（AppShell）的纯逻辑：断点形态、本地记忆、导航分组、命令面板筛选。
//
// 设计稿 §6：桌面（≥1100）是完整侧栏、761–1099 是 64px 图标栏、≤760 是底部 5 标签 + 更多抽屉。
// 三种形态按「视口宽度」决定，不做容器查询；侧栏是否收起、节点区是否展开都记在 localStorage
// （`gofer.shell`），与设计稿 §16 的两条追加要求一致。这里只放纯函数，DOM / 组件在 ui/AppShell.vue。

// 断点：与设计稿 §4.3 一致（只保留这三个口径）。
export const SHELL_MOBILE_MAX = 760
export const SHELL_RAIL_MAX = 1099

export type ShellForm = 'tabs' | 'rail' | 'desktop'

// shellForm：视口宽度 → 导航形态。取不到宽度（0 / 负数）按桌面处理，避免 SSR 抖动成手机布局。
export function shellForm(width: number): ShellForm {
  if (!(width > 0)) return 'desktop'
  if (width <= SHELL_MOBILE_MAX) return 'tabs'
  if (width <= SHELL_RAIL_MAX) return 'rail'
  return 'desktop'
}

// hasSidebar / hasTabBar：两种形态各自挂哪个导航容器。
export function hasSidebar(form: ShellForm): boolean {
  return form !== 'tabs'
}
export function hasTabBar(form: ShellForm): boolean {
  return form === 'tabs'
}

// sidebarExpanded：侧栏是否展开成 236px 的完整形态。
// 手机没有侧栏；图标栏宽度下强制图标栏（本地记忆只在桌面档生效，缩到平板再回来还认原来的选择）。
export function sidebarExpanded(form: ShellForm, prefs: ShellPrefs): boolean {
  if (form === 'tabs') return false
  if (form === 'rail') return false
  return !prefs.collapsed
}

// ---- 本地记忆（localStorage `gofer.shell`） ----

export interface ShellPrefs {
  // 侧栏收起成图标栏（桌面档）
  collapsed: boolean
  // 侧栏底部运行节点区展开（默认收起，只显示一行摘要）
  nodesOpen: boolean
}

export const SHELL_PREFS_KEY = 'gofer.shell'

export const DEFAULT_SHELL_PREFS: ShellPrefs = { collapsed: false, nodesOpen: false }

// 只依赖 getItem / setItem，测试里传内存实现即可。
export interface StorageLike {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

function defaultStorage(): StorageLike | null {
  try {
    if (typeof localStorage === 'undefined') return null
    return localStorage
  } catch {
    return null
  }
}

// parseShellPrefs：坏值一律回落默认（隐私模式、手改过的值）。
export function parseShellPrefs(raw: string | null | undefined): ShellPrefs {
  if (!raw) return { ...DEFAULT_SHELL_PREFS }
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { ...DEFAULT_SHELL_PREFS }
  }
  if (!parsed || typeof parsed !== 'object') return { ...DEFAULT_SHELL_PREFS }
  const o = parsed as Record<string, unknown>
  return {
    collapsed: o.collapsed === true,
    nodesOpen: o.nodesOpen === true,
  }
}

export function readShellPrefs(storage: StorageLike | null = defaultStorage()): ShellPrefs {
  if (!storage) return { ...DEFAULT_SHELL_PREFS }
  try {
    return parseShellPrefs(storage.getItem(SHELL_PREFS_KEY))
  } catch {
    return { ...DEFAULT_SHELL_PREFS }
  }
}

export function writeShellPrefs(prefs: ShellPrefs, storage: StorageLike | null = defaultStorage()): void {
  if (!storage) return
  try {
    storage.setItem(SHELL_PREFS_KEY, JSON.stringify({ collapsed: prefs.collapsed, nodesOpen: prefs.nodesOpen }))
  } catch {
    // 隐私模式 / 禁用存储：仅当前会话生效
  }
}

// ---- 导航分组（设计稿 §6.1） ----
//
// 图标只做形态提示（纯字符，不加载图标库），名称一律走 i18n 的 nav.*，与顶栏 / 页面标题同源。
export interface ShellNavItem {
  // 路由；/jobs 是任务列表（旧 /board 重定向过来）
  to: string
  // i18n key（nav.*）
  key: string
  // 图标栏 / 手机标签用的字形
  glyph: string
}

export interface ShellNavGroup {
  // 分组名（i18n key）
  key: string
  items: ShellNavItem[]
}

export const SHELL_NAV_GROUPS: readonly ShellNavGroup[] = [
  {
    key: 'nav.group.core',
    items: [
      { to: '/today', key: 'nav.today', glyph: '◉' },
      { to: '/workbench', key: 'nav.workbench', glyph: '▤' },
      { to: '/work', key: 'nav.work', glyph: '▥' },
      { to: '/plans', key: 'nav.plans', glyph: '◇' },
      { to: '/jobs', key: 'nav.jobs', glyph: '⧉' },
      { to: '/sessions', key: 'nav.sessions', glyph: '⌁' },
    ],
  },
  {
    key: 'nav.group.collab',
    items: [
      { to: '/mail', key: 'nav.mail', glyph: '✉' },
      { to: '/review', key: 'nav.review', glyph: '✓' },
      { to: '/issues', key: 'nav.issues', glyph: '⚑' },
      { to: '/dashboard', key: 'nav.dashboard', glyph: '▦' },
    ],
  },
  {
    key: 'nav.group.auto',
    items: [
      { to: '/workflows', key: 'nav.workflows', glyph: '⇉' },
      { to: '/schedules', key: 'nav.schedules', glyph: '◷' },
    ],
  },
  {
    key: 'nav.group.infra',
    items: [
      { to: '/runners', key: 'nav.runners', glyph: '⬡' },
      { to: '/agents', key: 'nav.agents', glyph: '◈' },
      { to: '/projects', key: 'nav.projects', glyph: '⌂' },
      { to: '/settings', key: 'nav.settings', glyph: '⚙' },
    ],
  },
]

// sidebarNavGroups：侧栏去掉手机专属项（手机「更多」里才放的主题/语言不在这里）。
export function sidebarNavGroups(): ShellNavGroup[] {
  return SHELL_NAV_GROUPS.map((g) => ({ key: g.key, items: [...g.items] }))
}

// 手机底部标签：今天 / 工作台 / 任务 / 计划 / 更多（§16-3：工作项放「更多」第一位）。
export const TAB_BAR_ITEMS: readonly ShellNavItem[] = [
  { to: '/today', key: 'nav.today', glyph: '◉' },
  { to: '/workbench', key: 'nav.workbench', glyph: '▤' },
  { to: '/jobs', key: 'nav.jobs', glyph: '⧉' },
  { to: '/plans', key: 'nav.plans', glyph: '◇' },
]

// moreItems：更多抽屉里的宫格，工作项排第一（§16-3）。
export function moreItems(): ShellNavItem[] {
  const first = { to: '/work', key: 'nav.work', glyph: '▥' }
  const rest: ShellNavItem[] = []
  for (const g of SHELL_NAV_GROUPS) {
    for (const it of g.items) {
      if (it.to === '/today' || it.to === '/workbench' || it.to === '/jobs' || it.to === '/plans' || it.to === '/work') continue
      rest.push(it)
    }
  }
  return [first, ...rest]
}

// navItemActive：当前路径是否落在该项上（含子页，如 /plans/:id、/jobs/:id）。
export function navItemActive(path: string, to: string): boolean {
  if (path === to) return true
  return path.startsWith(`${to}/`)
}

// tabBarActive / moreActive：手机标签与「更多」的高亮。
export function tabBarActive(path: string): string | null {
  for (const it of TAB_BAR_ITEMS) if (navItemActive(path, it.to)) return it.to
  return null
}

export function moreActive(path: string): boolean {
  const hit = tabBarActive(path)
  return hit === null && path !== '/access'
}

// ---- 「待我决策」计数（合并 worker 申请） ----
//
// EscalationBell 的决策数 + EnrollmentBell 的 worker 申请数合成一个数字（§16-4）。
// 负数/NaN 当 0，避免接口异常时显示「待我决策 -1」。
export function needsMeTotal(decisions: number, enrollments: number): number {
  const a = Number.isFinite(decisions) && decisions > 0 ? Math.floor(decisions) : 0
  const b = Number.isFinite(enrollments) && enrollments > 0 ? Math.floor(enrollments) : 0
  return a + b
}

// ---- 命令面板（§6.5，P1 只做页面跳转与 p: 项目前缀） ----

export interface PalettePage {
  key: string
  to: string
  // 支持 ?project= 过滤（§6.4 的列表页）
  projectFilter: boolean
}

export const PALETTE_PAGES: readonly PalettePage[] = [
  { key: 'nav.today', to: '/today', projectFilter: true },
  { key: 'nav.workbench', to: '/workbench', projectFilter: true },
  { key: 'nav.work', to: '/work', projectFilter: true },
  { key: 'nav.plans', to: '/plans', projectFilter: true },
  { key: 'nav.jobs', to: '/jobs', projectFilter: true },
  { key: 'nav.sessions', to: '/sessions', projectFilter: true },
  { key: 'nav.mail', to: '/mail', projectFilter: false },
  { key: 'nav.review', to: '/review', projectFilter: true },
  { key: 'nav.issues', to: '/issues', projectFilter: true },
  { key: 'nav.dashboard', to: '/dashboard', projectFilter: true },
  { key: 'nav.workflows', to: '/workflows', projectFilter: true },
  { key: 'nav.schedules', to: '/schedules', projectFilter: true },
  { key: 'nav.runners', to: '/runners', projectFilter: false },
  { key: 'nav.agents', to: '/agents', projectFilter: false },
  { key: 'nav.projects', to: '/projects', projectFilter: false },
  { key: 'nav.settings', to: '/settings', projectFilter: false },
]

// parsePaletteQuery：`p:<项目>` 前缀（可带后面的关键词），其余是普通关键词。
export function parsePaletteQuery(raw: string): { project: string; text: string } {
  const q = (raw ?? '').trim()
  const m = /^p:(\S*)\s*(.*)$/i.exec(q)
  if (!m) return { project: '', text: q.toLowerCase() }
  return { project: m[1] ?? '', text: (m[2] ?? '').toLowerCase() }
}

// paletteHits：按关键词过滤页面；有 p: 前缀时只留支持项目过滤的页面，并把项目写进目标 URL。
export function paletteHits(
  raw: string,
  pages: readonly PalettePage[] = PALETTE_PAGES,
  label: (key: string) => string = (key) => key,
): Array<{ to: string; label: string }> {
  const { project, text } = parsePaletteQuery(raw)
  const out: Array<{ to: string; label: string }> = []
  for (const p of pages) {
    if (project && !p.projectFilter) continue
    const name = label(p.key)
    if (text && !`${name} ${p.to}`.toLowerCase().includes(text)) continue
    out.push({ to: withProject(p.to, project), label: name })
  }
  return out
}

// withProject：把项目筛选写进 URL（唯一真源，§6.4）；空项目原样返回。
export function withProject(path: string, project: string): string {
  if (!project) return path
  const sep = path.includes('?') ? '&' : '?'
  return `${path}${sep}project=${encodeURIComponent(project)}`
}
