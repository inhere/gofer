// PageDict 是按页面拆分的文案：zh 与 en 两份平铺字典放在同一个文件里。
export interface PageDict {
  zh: Record<string, string>
  en: Record<string, string>
}
