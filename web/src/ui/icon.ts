// 图标（issue gofer-h355）：统一尺寸档位 + 导航图标表。
//
// 图标来自 lucide（@lucide/vue，ISC 许可）：每个图标是一个函数组件，按需 import，
// 打包时只留用到的那些，不依赖外网。颜色一律 currentColor，跟随所在文字。
//
// 尺寸三档与 tokens.css 的 --icon-sm / --icon / --icon-lg 一一对应
// （icon.test.ts 校验两处数值一致）；线宽统一 1.75——lucide 默认 2 在 14–16px 上偏重。
//
// 导航用语义化名字（NavIcon）而不是 lucide 图标名：换图标只改这张表，
// utils/shell.ts 与外壳组件都不动。
import {
  Bot,
  CalendarClock,
  ChartColumn,
  CircleDot,
  ClipboardCheck,
  ClipboardList,
  Ellipsis,
  FolderKanban,
  LayoutGrid,
  ListChecks,
  Mail,
  MessagesSquare,
  Milestone,
  Server,
  Settings,
  Sun,
  Workflow,
  type LucideIcon,
} from '@lucide/vue'

// 三档尺寸；sm 用于行内与表格，md 是默认，lg 用于手机标签栏与宫格。
export type IconSize = 'sm' | 'md' | 'lg'

export const ICON_PX: Record<IconSize, number> = { sm: 14, md: 16, lg: 20 }

// 统一线宽（lucide 的 stroke-width，单位是 24×24 视口里的用户单位）。
export const ICON_STROKE = 1.75

// iconPx：档位或显式像素数 → 传给 lucide 的 size。
export function iconPx(size: IconSize | number = 'md'): number {
  return typeof size === 'number' ? size : ICON_PX[size]
}

// 导航图标：顺序与分组见设计稿 §6.1。
export type NavIcon =
  | 'today'
  | 'workbench'
  | 'work'
  | 'plans'
  | 'jobs'
  | 'sessions'
  | 'mail'
  | 'review'
  | 'issues'
  | 'dashboard'
  | 'workflows'
  | 'schedules'
  | 'runners'
  | 'agents'
  | 'projects'
  | 'settings'
  | 'more'

export const NAV_ICONS: Record<NavIcon, LucideIcon> = {
  today: Sun, // 今天：当天
  workbench: LayoutGrid, // 工作台：多面板
  work: ListChecks, // 工作项：勾选清单
  plans: Milestone, // 计划：里程碑
  jobs: ClipboardList, // 任务：任务清单
  sessions: MessagesSquare, // 会话：对话
  mail: Mail, // 信箱
  review: ClipboardCheck, // 待验收
  issues: CircleDot, // Issues：议题圆点
  dashboard: ChartColumn, // 统计
  workflows: Workflow, // 工作流
  schedules: CalendarClock, // 定时
  runners: Server, // 运行节点
  agents: Bot, // Agents
  projects: FolderKanban, // 项目
  settings: Settings, // 设置
  more: Ellipsis, // 手机「更多」
}

// navIcon：按语义名取图标组件（外壳模板用；名字来自 utils/shell.ts 的 type 检查）。
export function navIcon(name: NavIcon): LucideIcon {
  return NAV_ICONS[name]
}
