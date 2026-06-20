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
import { initTheme } from './store/theme'

// 挂载前应用主题（持久化偏好或跟随系统 prefers-color-scheme），尽量减少首屏闪烁
initTheme()

// 注册 401 处理：client 已清 token，这里负责跳转到接入页
setUnauthorizedHandler(() => {
  if (router.currentRoute.value.path !== '/access') {
    router.replace({ path: '/access' })
  }
})

createApp(App).use(router).mount('#app')
