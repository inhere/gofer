import type { RouteRecordRaw } from 'vue-router'
import { legacyMemoryQuery } from './utils/memories'

// 路由表（与 router.ts 分离：本文件不建 router，测试可直接在 node 环境导入）。
export const routes: RouteRecordRaw[] = [
  // N3：默认落地页是「今天」（决策中心）；Dashboard 保留为统计页。
  { path: '/', redirect: '/today' },
  {
    path: '/access',
    name: 'access',
    component: () => import('./views/Access.vue'),
    meta: { public: true },
  },
  { path: '/today', name: 'today', component: () => import('./views/Today.vue') },
  { path: '/dashboard', name: 'dashboard', component: () => import('./views/Dashboard.vue') },
  { path: '/workbench', name: 'workbench', component: () => import('./views/Workbench.vue') },
  // 任务列表（设计稿 §7.1 / §16-3）：旧路径是 /board，视觉上它不是看板而是一张表，
  // 改名为 /jobs，旧链接带原 query 重定向（下面的 /board 路由）。
  { path: '/jobs', name: 'jobs', component: () => import('./views/Board.vue') },
  { path: '/board', redirect: (to) => ({ path: '/jobs', query: to.query }) },
  // REV-01 验收台：待验收 job 队列（列表 + 行内裁决），详情面板在 /jobs/:id。
  { path: '/review', name: 'review', component: () => import('./views/ReviewQueue.vue') },
  { path: '/sessions', name: 'sessions', component: () => import('./views/Sessions.vue') },
  // 信箱（design 2026-10-16「二、gofer 信箱」）：会话 / job / 调用方之间的信件往来。
  // 深链：/mail?state=held（待指派，Today 卡的入口）、/mail?session=<sid>（会话抽屉）。
  { path: '/mail', name: 'mail', component: () => import('./views/Mail.vue') },
  // W1：工作项总览（一张卡 = 一件事，跨会话；按状态 / 工作区看）。
  { path: '/work', name: 'work', component: () => import('./views/Work.vue') },
  { path: '/new', name: 'new-job', component: () => import('./views/NewJob.vue') },
  // 任务详情：够宽时左边列表 + 右边详情并排（宿主页按宽度决定，设计稿 §7.1 / §16）。
  {
    path: '/jobs/:id',
    name: 'job-detail',
    component: () => import('./views/JobsView.vue'),
    props: true,
  },
  { path: '/workflows', name: 'workflows', component: () => import('./views/Workflows.vue') },
  {
    path: '/workflows/new',
    name: 'new-workflow',
    component: () => import('./views/NewWorkflow.vue'),
  },
  {
    path: '/workflows/:id',
    name: 'workflow-detail',
    component: () => import('./views/WorkflowDetail.vue'),
    props: true,
  },
  { path: '/plans', name: 'plans', component: () => import('./views/Plans.vue') },
  {
    path: '/issues',
    name: 'issues',
    component: () => import('./views/Issues.vue'),
    // 记忆页签独立成 /memory（UI 重设计 P6）：旧链接 /issues?tab=memories… 带全部参数跳过去
    // （今天页的记忆整理卡就是这个形式，带 memory_scope / tracker / memory）。
    beforeEnter: (to) => {
      const query = legacyMemoryQuery(to.query)
      return query ? { path: '/memory', query } : true
    },
  },
  // 记忆：仓库（.gofer/tracker） / 项目 / 全局三档作用域，原 Issues 页的记忆页签。
  { path: '/memory', name: 'memory', component: () => import('./views/Memory.vue') },
  {
    path: '/plans/:id',
    name: 'plan-detail',
    component: () => import('./views/PlanDetail.vue'),
    props: true,
  },
  { path: '/schedules', name: 'schedules', component: () => import('./views/Schedules.vue') },
  {
    path: '/schedules/new',
    name: 'new-schedule',
    component: () => import('./views/NewSchedule.vue'),
  },
  { path: '/projects', name: 'projects', component: () => import('./views/Projects.vue') },
  { path: '/agents', name: 'agents', component: () => import('./views/Agents.vue') },
  // JOB-10：server 技能库（列表/详情/导入/更新/删除/导出）。
  { path: '/skills', redirect: '/settings/skills' },
  { path: '/runners', name: 'runners', component: () => import('./views/Runners.vue') },
  { path: '/cluster', redirect: '/runners' },
  // WEB-12：设置区改成二级菜单（views/settings/SettingsLayout.vue）——「⚙ 设置」进 /settings，
  // 默认落到配置管理；tunnels / about 与它同级。加设置页 = 这里加一条 + 布局的 sections 加一行。
  {
    path: '/settings',
    component: () => import('./views/settings/SettingsLayout.vue'),
    children: [
      { path: '', redirect: '/settings/config' },
      {
        path: 'config',
        name: 'settings-config',
        component: () => import('./views/Config.vue'),
      },
      // JOB-06①：强制规则库（列表 / 编辑预览 / 绑定反查）。
      {
        path: 'rules',
        name: 'settings-rules',
        component: () => import('./views/settings/Rules.vue'),
      },
      // MCP-06：MCP 注册表（只读 + server 本机测试）。
      {
        path: 'mcp',
        name: 'settings-mcp',
        component: () => import('./views/settings/McpServers.vue'),
      },
      {
        path: 'tunnels',
        name: 'settings-tunnels',
        component: () => import('./views/settings/Tunnels.vue'),
      },
      {
        path: 'notifications',
        name: 'settings-notifications',
        component: () => import('./views/settings/Notifications.vue'),
      },
      {
        path: 'permissions',
        name: 'settings-permissions',
        component: () => import('./views/settings/Permissions.vue'),
      },
      {
        path: 'work',
        name: 'settings-work',
        component: () => import('./views/settings/WorkSettings.vue'),
      },
      // K5：扩展设置（数据库里的降噪阈值 / 排除名单 / 提示词覆盖，带导入导出）。
      {
        path: 'extensions',
        name: 'settings-extensions',
        component: () => import('./views/settings/Extensions.vue'),
      },
      {
        path: 'skills',
        name: 'settings-skills',
        component: () => import('./views/Skills.vue'),
      },
      {
        path: 'about',
        name: 'settings-about',
        component: () => import('./views/settings/About.vue'),
      },
    ],
  },
  // 旧书签/旧链接（R3 起「⚙ 设置」指向的地址）继续可用：重定向到新的配置管理页。
  { path: '/config', redirect: '/settings/config' },
]
