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
  { path: '/schedules', name: 'schedules', component: () => import('./views/Schedules.vue') },
  {
    path: '/schedules/new',
    name: 'new-schedule',
    component: () => import('./views/NewSchedule.vue'),
  },
  { path: '/drivers', name: 'drivers', component: () => import('./views/Drivers.vue') },
  {
    path: '/drivers/:id',
    name: 'driver-inbox',
    component: () => import('./views/DriverInbox.vue'),
    props: true,
  },
  { path: '/projects', name: 'projects', component: () => import('./views/Projects.vue') },
  { path: '/agents', name: 'agents', component: () => import('./views/Agents.vue') },
  { path: '/runners', name: 'runners', component: () => import('./views/Runners.vue') },
  { path: '/cluster', name: 'cluster', component: () => import('./views/Cluster.vue') },
  { path: '/config', name: 'config', component: () => import('./views/Config.vue') },
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
