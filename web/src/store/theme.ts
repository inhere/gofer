import { ref } from 'vue'

// 主题切换：默认跟随系统 prefers-color-scheme；用户一旦显式选择即以 localStorage 持久化并固定。
export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'gofer.theme'

// 全局共享的当前主题（模块级单例 ref）
const theme = ref<Theme>('dark')
// 用户是否已显式选择：固定后不再跟随系统变化
let userPinned = false

function systemPrefersLight(): boolean {
  return typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia('(prefers-color-scheme: light)').matches
}

// 写入 <html data-theme>，驱动 tokens.css 变量覆盖（dark 为默认值）
function apply(t: Theme) {
  theme.value = t
  document.documentElement.setAttribute('data-theme', t)
}

// 启动期调用（main.ts，挂载前）：读取持久化偏好，否则跟随系统，并监听系统变化
export function initTheme() {
  let stored: string | null = null
  try {
    stored = localStorage.getItem(STORAGE_KEY)
  } catch {
    stored = null
  }
  if (stored === 'light' || stored === 'dark') {
    userPinned = true
    apply(stored)
  } else {
    apply(systemPrefersLight() ? 'light' : 'dark')
  }

  // 未显式选择时跟随系统切换（已固定则忽略）
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    const mq = window.matchMedia('(prefers-color-scheme: light)')
    const onChange = (e: MediaQueryListEvent) => {
      if (!userPinned) apply(e.matches ? 'light' : 'dark')
    }
    if (mq.addEventListener) mq.addEventListener('change', onChange)
    else if (mq.addListener) mq.addListener(onChange)
  }
}

export function useTheme() {
  return { theme }
}

// 显式设定主题：固定偏好并持久化
export function setTheme(t: Theme) {
  userPinned = true
  try {
    localStorage.setItem(STORAGE_KEY, t)
  } catch {
    // 隐私模式/禁用存储：仅当前会话生效
  }
  apply(t)
}

export function toggleTheme() {
  setTheme(theme.value === 'dark' ? 'light' : 'dark')
}
