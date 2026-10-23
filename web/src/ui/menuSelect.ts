// 下拉菜单的纯逻辑（设计稿 §8：统一的下拉菜单组件，取代原生 <select>）：
// 当前值 → 高亮项、键盘上下移动（循环）、Esc 关闭。组件只负责渲染与开关状态。
export interface MenuOption {
  value: string
  label: string
  /** 当前值：带勾、点击不再发 change */
  disabled?: boolean
  title?: string
}

export const NO_INDEX = -1

// menuIndex：当前值在选项里的下标；找不到返回 -1（触发器显示占位文字）。
export function menuIndex(options: MenuOption[], value: string): number {
  return options.findIndex((o) => o.value === value)
}

// stepIndex：键盘上下移动，首尾循环；空列表返回 -1。
export function stepIndex(index: number, delta: number, count: number): number {
  if (count <= 0) return NO_INDEX
  const from = index < 0 ? (delta > 0 ? -1 : 0) : index
  return (from + delta + count) % count
}

// 可选项下标：跳过 disabled 的项（当前值不可再选）。
export function firstEnabledIndex(options: MenuOption[], from = 0, delta = 1): number {
  if (!options.length) return NO_INDEX
  let i = ((from % options.length) + options.length) % options.length
  for (let n = 0; n < options.length; n++) {
    if (!options[i].disabled) return i
    i = (i + delta + options.length) % options.length
  }
  return NO_INDEX
}

// 从当前下标起按方向找下一个可选项。
export function nextEnabledIndex(options: MenuOption[], index: number, delta: number): number {
  if (!options.length) return NO_INDEX
  const start = index < 0 ? (delta > 0 ? -1 : 0) : index
  for (let n = 0; n < options.length; n++) {
    const i = (start + delta * (n + 1) + options.length * (n + 1)) % options.length
    if (!options[i]?.disabled) return i
  }
  return NO_INDEX
}
