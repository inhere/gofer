// 极简 i18n（设计稿 §9.1，机制取自初稿）：自己写，不依赖库。
//
// - 平铺字典（en / zh），key 用英文语义命名，见 en.ts 头部。
// - t(key, params)：插值只支持 {name}。
// - 缺 key 时回退到中文；中文也没有就原样返回 key，并在开发环境 warn。
// - 当前语言是 store/locale.ts 的单例 ref，切换后模板自动重算。
//
// key 超过约 800 个、或需要复数规则时再换 vue-i18n（初稿 D3）。
import { locale, type Locale } from '../store/locale'
import { en } from './en'
import { zh } from './zh'

export type Dict = Record<string, string>
export type Dicts = Record<Locale, Dict>

// 回退语言：缺 key 时用中文（中文是作者维护的主字典）。
export const FALLBACK_LOCALE: Locale = 'zh-CN'

export const dicts: Dicts = {
  'zh-CN': zh,
  'en-US': en,
}

const warned = new Set<string>()

function lookup(key: string, loc: Locale): string | undefined {
  // 字典是普通对象，key 里可能带点号，直接按整串取（扁平字典，不做嵌套解析）。
  return Object.prototype.hasOwnProperty.call(dicts[loc], key) ? dicts[loc][key] : undefined
}

// translate：指定语言的翻译（测试和 format 用，不读全局 ref）。
export function translate(key: string, params?: Record<string, string | number>, loc: Locale = locale.value): string {
  let text = lookup(key, loc)
  if (text === undefined && loc !== FALLBACK_LOCALE) {
    text = lookup(key, FALLBACK_LOCALE)
  }
  if (text === undefined) {
    if (import.meta.env?.DEV && !warned.has(key)) {
      warned.add(key)
      console.warn(`[i18n] 缺少文案：${key}`)
    }
    return key
  }
  return params ? interpolate(text, params) : text
}

// 插值：{name} 形式的占位符；没给的参数原样保留，便于发现漏传。
export function interpolate(text: string, params: Record<string, string | number>): string {
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => {
    const v = params[name]
    return v === undefined ? whole : String(v)
  })
}

// t：当前语言的翻译，组件与模板里用这个。
export function t(key: string, params?: Record<string, string | number>): string {
  return translate(key, params, locale.value)
}

// te：当前语言字典里有这个 key 吗（用于「有就翻译、没有就用原始值」的场景）。
export function te(key: string): boolean {
  return lookup(key, locale.value) !== undefined || lookup(key, FALLBACK_LOCALE) !== undefined
}

// 组件里一次拿到语言与翻译函数。
export function useI18n() {
  return { locale, t, te, translate }
}
