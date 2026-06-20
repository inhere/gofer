import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { hasToken } from './store/auth'

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/board' },
  {
    path: '/access',
    name: 'access',
    component: () => import('./views/Access.vue'),
    meta: { public: true },
  },
  { path: '/board', name: 'board', component: () => import('./views/Board.vue') },
  {
    path: '/jobs/:id',
    name: 'job-detail',
    component: () => import('./views/JobDetail.vue'),
    props: true,
  },
  { path: '/projects', name: 'projects', component: () => import('./views/Projects.vue') },
  { path: '/agents', name: 'agents', component: () => import('./views/Agents.vue') },
  { path: '/runners', name: 'runners', component: () => import('./views/Runners.vue') },
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
