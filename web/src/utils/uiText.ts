// 界面文字的唯一出处（用户 2026-10-19 的口径）：导航、页面标题、表头、筛选标签等给人看的
// 文案一律中文，并且只在这里定义一次，各页面引用，避免同一个词在各页各写一份。
//
// 不改的东西：命令文本（`gofer job run`）、配置键、接口字段名、日志原文、agent 输出——
// 那些是"值"不是"界面文字"，照旧英文。Issues / Agents / Runners / Agent / Runner / Prompt
// 是专有名词，保留英文（见 NAV / PAGE 里的同名项）。
export const NAV = {
  today: '今天',
  workbench: '工作台',
  work: '工作项',
  plans: '计划',
  jobs: '任务',
  sessions: '会话',
  dashboard: '统计',
  projects: '项目',
  workflows: '工作流',
  schedules: '定时',
  issues: 'Issues',
  agents: 'Agents',
  runners: 'Runners',
  mail: '信箱',
} as const

// PAGE：页面标题（h1）。与导航同名的地方直接取 NAV，只有确实不同的才单列
// （任务列表页标题是「任务看板」，导航项是「任务」）。
export const PAGE = {
  today: NAV.today,
  workbench: NAV.workbench,
  work: NAV.work,
  plans: NAV.plans,
  board: '任务看板',
  sessions: NAV.sessions,
  dashboard: NAV.dashboard,
  projects: NAV.projects,
  workflows: NAV.workflows,
  schedules: NAV.schedules,
  issues: NAV.issues,
  agents: NAV.agents,
  runners: NAV.runners,
  review: '待验收',
  skills: '技能',
  rules: '规则',
  settings: '设置',
  newJob: '新建任务',
} as const

// 返回链接（面包屑）文案：`← 任务看板`。
export const BACK = {
  board: `← ${PAGE.board}`,
  schedules: `← ${PAGE.schedules}`,
  workflows: `← ${PAGE.workflows}`,
  plans: `← ${PAGE.plans}`,
} as const

// 列表表头里反复出现的列名。各页在自己的 thead 里组合（`任务 · 标题 / ID` 这类拼接
// 仍写在页面里，因为它表达的是该页这一列的语义）。
export const COL = {
  status: '状态',
  project: '项目',
  agent: 'Agent',
  runner: 'Runner',
  title: '标题',
  tags: '标签',
  progress: '进度',
  updated: '更新',
  actions: '操作',
  time: '时间',
  plan: '计划',
  name: '名称',
} as const
