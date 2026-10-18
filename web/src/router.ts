import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { hasToken } from './store/auth'

const routes: RouteRecordRaw[] = [
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
  { path: '/board', name: 'board', component: () => import('./views/Board.vue') },
  // REV-01 验收台：待验收 job 队列（列表 + 行内裁决），详情面板在 /jobs/:id。
  { path: '/review', name: 'review', component: () => import('./views/ReviewQueue.vue') },
  { path: '/sessions', name: 'sessions', component: () => import('./views/Sessions.vue') },
  // 信箱（design 2026-10-16「二、gofer 信箱」）：会话 / job / 调用方之间的信件往来。
  // 深链：/mail?state=held（待指派，Today 卡的入口）、/mail?session=<sid>（会话抽屉）。
  { path: '/mail', name: 'mail', component: () => import('./views/Mail.vue') },
  // W1：工作项总览（一张卡 = 一件事，跨会话；按状态 / 工作区看）。
  { path: '/work', name: 'work', component: () => import('./views/Work.vue') },
  { path: '/new', name: 'new-job', component: () => import('./views/NewJob.vue') },
  {
    path: '/jobs/:id',
    name: 'job-detail',
    component: () => import('./views/JobDetail.vue'),
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
  { path: '/issues', name: 'issues', component: () => import('./views/Issues.vue') },
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

const router = createRouter({
  history: createWebHistory('/'),
  routes,
})

// 全局守卫：无 token 且目标非 /access -> 跳 /access
router.beforeEach((to) => {
  if (to.path === '/access') {
    return true
  }
  if (!hasToken()) {
    return { path: '/access' }
  }
  return true
})

export default router
