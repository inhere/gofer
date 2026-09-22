import { createApp } from 'vue'

// 离线字体（不依赖外网 CDN）
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'

import './styles/tokens.css'

import App from './App.vue'
import router from './router'
import { setUnauthorizedHandler } from './store/auth'
import { markStaleBuild } from './store/staleBuild'
import { initTheme } from './store/theme'

// 挂载前应用主题（持久化偏好或跟随系统 prefers-color-scheme），尽量减少首屏闪烁
initTheme()

// 注册 401 处理：client 已清 token，这里负责跳转到接入页
setUnauthorizedHandler(() => {
  if (router.currentRoute.value.path !== '/access') {
    router.replace({ path: '/access' })
  }
})

// F8：升级换包后路由懒加载的 chunk 名字（内容 hash）已经变了，浏览器按旧名字去取会
// 404，动态 import 直接 reject，导航 promise 被拒——用户看到的就是「菜单点不动」，
// 控制台只有一行拉取/MIME 错误。这里捕获这类错误，重载一次拿到新 shell 与新 chunk。
const CHUNK_ERROR_PATTERNS = [
  'failed to fetch dynamically imported module',
  'importing a module script failed',
  'error loading dynamically imported module',
  'chunkloaderror',
]

function isChunkLoadError(err: unknown): boolean {
  const msg = (err instanceof Error ? err.message : String(err ?? '')).toLowerCase()
  return CHUNK_ERROR_PATTERNS.some((p) => msg.includes(p))
}

const RELOAD_STAMP_PREFIX = 'gofer:chunk-reload'
const RELOAD_COOLDOWN_MS = 60_000
// 一次 chunk 失败会从两条路报上来（动态 import → router.onError，modulepreload 的
// <link> → vite:preloadError），本文档决定重载后其余并发错误直接忽略，不再重复处理。
let reloadIssued = false

// 构建标记：index.html 里入口 module script 的 src（内容 hash 一变就是新构建）。
// 读不到就退回本次页面加载时刻，语义仍是「同一会话 60s 内只自动重载一次」。
function buildMarker(): string {
  const src = document.querySelector('script[type="module"][src]')?.getAttribute('src')
  return src || String(Math.floor(performance.timeOrigin || Date.now()))
}

function reloadOnce(): void {
  if (reloadIssued) {
    return
  }
  const key = `${RELOAD_STAMP_PREFIX}:${buildMarker()}`
  const now = Date.now()
  let last = 0
  try {
    last = Number(window.sessionStorage.getItem(key) ?? 0)
  } catch {
    // sessionStorage 不可用（隐私模式等）：记不住跨文档冷却，自动重载会退化成无限
    // 刷新——宁可不自动重载，只提示手动刷新。
    console.warn('[gofer] chunk 加载失败，但浏览器不允许记录重载标记；请手动刷新页面。')
    markStaleBuild()
    return
  }
  if (now - last < RELOAD_COOLDOWN_MS) {
    console.warn(
      '[gofer] 旧版本 chunk 加载失败，但 60s 内已自动重载过一次，不再重试；请手动刷新页面。',
    )
    markStaleBuild()
    return
  }
  reloadIssued = true
  try {
    window.sessionStorage.setItem(key, String(now))
  } catch {
    // 读得到却写不进去（配额等）：只影响跨文档冷却，本文档仍只重载一次
  }
  window.location.reload()
}

router.onError((err) => {
  if (isChunkLoadError(err)) {
    reloadOnce()
  }
})

// vite 的预加载错误（modulepreload 的 <link> 拿不到文件）不走 router.onError。
window.addEventListener('vite:preloadError', (e) => {
  e.preventDefault()
  reloadOnce()
})

createApp(App).use(router).mount('#app')
