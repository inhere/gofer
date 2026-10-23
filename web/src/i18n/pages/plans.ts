// plans 页的文案（UI 重设计 P3）。每个页面一个文件、zh 与 en 并列，避免并行迁移时
// 多个分支同时改 zh.ts / en.ts 冲突。key 前缀 `plans.`，命名规范见设计稿 §9.2。
import type { PageDict } from './types'

export const plans: PageDict = {
  zh: {},
  en: {},
}
