import { createRouter, createWebHistory } from 'vue-router'
import { routes } from './router.routes'
import { hasToken, requestTokenFromPeers } from './store/auth'

const router = createRouter({
  history: createWebHistory('/'),
  routes,
})

// 全局守卫：无 token 且目标非 /access -> 跳 /access
router.beforeEach(async (to) => {
  if (to.path === '/access') {
    return true
  }
  if (!hasToken()) {
    // 从通知链接打开的新标签页：先向已登录的其他标签页要 token，要不到才去 /access。
    if (await requestTokenFromPeers()) {
      return true
    }
    return { path: '/access', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
