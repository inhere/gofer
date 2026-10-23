// 界面文字的唯一出处（用户 2026-10-19 的口径）：导航、页面标题、表头、筛选标签等给人看的
// 文案一律中文，并且只在这里定义一次，各页面引用，避免同一个词在各页各写一份。
//
// 不改的东西：命令文本（`gofer job run`）、配置键、接口字段名、日志原文、agent 输出——
// 那些是"值"不是"界面文字"，照旧英文。Issues / Agents / Runners / Agent / Runner / Prompt
// 是专有名词，保留英文（见 NAV / PAGE 里的同名项）。
//
// P6-b3：文案走 i18n（字典 ui.nav.* / ui.page.* / ui.col.* / ui.back.*）。属性都写成 getter，
// 模板 / computed 里读取时才取当前语言，切换语言即时生效。
import { t } from '../i18n'

export const NAV = {
  get today() { return t('ui.nav.today') },
  get workbench() { return t('ui.nav.workbench') },
  get work() { return t('ui.nav.work') },
  get plans() { return t('ui.nav.plans') },
  get jobs() { return t('ui.nav.jobs') },
  get sessions() { return t('ui.nav.sessions') },
  get dashboard() { return t('ui.nav.dashboard') },
  get projects() { return t('ui.nav.projects') },
  get workflows() { return t('ui.nav.workflows') },
  get schedules() { return t('ui.nav.schedules') },
  get issues() { return 'Issues' },
  get agents() { return 'Agents' },
  get runners() { return 'Runners' },
  get mail() { return t('ui.nav.mail') },
}

// PAGE：页面标题（h1）。与导航同名的地方直接取 NAV，只有确实不同的才单列
// （任务列表页标题是「任务看板」，导航项是「任务」）。
export const PAGE = {
  get today() { return NAV.today },
  get workbench() { return NAV.workbench },
  get work() { return NAV.work },
  get plans() { return NAV.plans },
  get board() { return t('ui.page.board') },
  get sessions() { return NAV.sessions },
  get dashboard() { return NAV.dashboard },
  get projects() { return NAV.projects },
  get workflows() { return NAV.workflows },
  get schedules() { return NAV.schedules },
  get issues() { return NAV.issues },
  get agents() { return NAV.agents },
  get runners() { return NAV.runners },
  get review() { return t('ui.page.review') },
  get skills() { return t('ui.page.skills') },
  get rules() { return t('ui.page.rules') },
  get settings() { return t('ui.page.settings') },
  get newJob() { return t('ui.page.newJob') },
}

// 返回链接（面包屑）文案：`← 任务看板`。
export const BACK = {
  get board() { return t('ui.back.board', { page: PAGE.board }) },
  get schedules() { return t('ui.back.schedules', { page: PAGE.schedules }) },
  get workflows() { return t('ui.back.workflows', { page: PAGE.workflows }) },
  get plans() { return t('ui.back.plans', { page: PAGE.plans }) },
}

// 列表表头里反复出现的列名。各页在自己的 thead 里组合（`任务 · 标题 / ID` 这类拼接
// 仍写在页面里，因为它表达的是该页这一列的语义）。
export const COL = {
  get status() { return t('ui.col.status') },
  get project() { return t('ui.col.project') },
  agent: 'Agent',
  runner: 'Runner',
  get title() { return t('ui.col.title') },
  get tags() { return t('ui.col.tags') },
  get progress() { return t('ui.col.progress') },
  get updated() { return t('ui.col.updated') },
  get actions() { return t('ui.col.actions') },
  get time() { return t('ui.col.time') },
  get plan() { return t('ui.col.plan') },
  get name() { return t('ui.col.name') },
}
