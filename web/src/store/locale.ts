// 界面语言（设计稿 §9.1）：模块级单例 ref，存 localStorage `gofer.locale`。
//
// 首次访问读 navigator.languages：以 en 开头取 en-US，其余一律 zh-CN。
// `?lang=` 只用于调试和分享，不写入 localStorage（刷新后回到用户自己的选择）。
// 挂载前调用 initLocale() 写 <html lang>，避免首屏语言属性闪烁（与 initTheme 同一位置）。
import { ref } from 'vue'

export type Locale = 'zh-CN' | 'en-US'

const STORAGE_KEY = 'gofer.locale'

export const SUPPORTED_LOCALES: readonly Locale[] = ['zh-CN', 'en-US']

// 全局共享的当前语言
export const locale = ref<Locale>('zh-CN')

function isLocale(v: unknown): v is Locale {
  return v === 'zh-CN' || v === 'en-US'
}

// 宽松解析：'en'、'en-GB'、'zh'、'zh-Hans' 都能落到两个受支持的语言之一。
export function normalizeLocale(input: string | undefined | null): Locale | null {
  if (!input) return null
  const tag = input.trim().toLowerCase()
  if (!tag) return null
  if (tag === 'en' || tag.startsWith('en-')) return 'en-US'
  if (tag === 'zh' || tag.startsWith('zh-')) return 'zh-CN'
  return null
}

function storedLocale(): Locale | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY)
    return isLocale(v) ? v : null
  } catch {
    return null
  }
}

// 浏览器语言偏好；拿不到（SSR / 老浏览器）就当中文。
export function browserLocale(): Locale {
  if (typeof navigator === 'undefined') return 'zh-CN'
  const langs = navigator.languages ?? (navigator.language ? [navigator.language] : [])
  for (const l of langs) {
    const n = normalizeLocale(l)
    if (n) return n
  }
  return 'zh-CN'
}

// URL 上的 ?lang=：只用于调试和分享
function urlLocale(): Locale | null {
  if (typeof window === 'undefined') return null
  try {
    return normalizeLocale(new URLSearchParams(window.location.search).get('lang'))
  } catch {
    return null
  }
}

// 写 <html lang>：无障碍读音、:lang() 选择器、拼写检查都用它。
export function applyLocaleTag(l: Locale = locale.value) {
  locale.value = l
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('lang', l)
  }
}

// 启动期调用（main.ts，挂载前）：?lang= > localStorage > 浏览器。
export function initLocale(): Locale {
  const fromUrl = urlLocale()
  const l = fromUrl ?? storedLocale() ?? browserLocale()
  applyLocaleTag(l)
  return l
}

// 用户显式切换：持久化。
export function setLocale(l: Locale) {
  try {
    localStorage.setItem(STORAGE_KEY, l)
  } catch {
    // 隐私模式/禁用存储：仅当前会话生效
  }
  applyLocaleTag(l)
}

// 两个语言之间切换（顶栏 / 「更多」里的语言开关）。
export function toggleLocale() {
  setLocale(locale.value === 'zh-CN' ? 'en-US' : 'zh-CN')
}
