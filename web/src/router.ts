import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { hasToken } from './store/auth'

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/dashboard' },
  {
    path: '/access',
    name: 'access',
    component: () => import('./views/Access.vue'),
    meta: { public: true },
  },
  { path: '/dashboard', name: 'dashboard', component: () => import('./views/Dashboard.vue') },
  { path: '/board', name: 'board', component: () => import('./views/Board.vue') },
  // REV-01 验收台：待验收 job 队列（列表 + 行内裁决），详情面板在 /jobs/:id。
  { path: '/review', name: 'review', component: () => import('./views/ReviewQueue.vue') },
  { path: '/sessions', name: 'sessions', component: () => import('./views/Sessions.vue') },
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
  { path: '/drivers', redirect: '/agents/presence' },
  {
    path: '/drivers/:id',
    redirect: (to) => `/agents/presence/${encodeURIComponent(String(to.params.id))}`,
  },
  { path: '/agents/presence', name: 'agent-presence', component: () => import('./views/Agents.vue') },
  {
    path: '/agents/presence/:id',
    name: 'driver-inbox',
    component: () => import('./views/DriverInbox.vue'),
    props: true,
  },
  { path: '/projects', name: 'projects', component: () => import('./views/Projects.vue') },
  { path: '/agents', name: 'agents', component: () => import('./views/Agents.vue') },
  // JOB-10：server 技能库（列表/详情/导入/更新/删除/导出）。
  { path: '/skills', name: 'skills', component: () => import('./views/Skills.vue') },
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
      {
        path: 'tunnels',
        name: 'settings-tunnels',
        component: () => import('./views/settings/Tunnels.vue'),
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
