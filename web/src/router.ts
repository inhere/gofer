import { createRouter, createWebHistory } from 'vue-router'
import { routes } from './router.routes'
import { hasToken } from './store/auth'

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
