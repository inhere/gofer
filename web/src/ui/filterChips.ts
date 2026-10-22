// 筛选 chip 的数据与选中逻辑（纯函数，单测覆盖）：单选再点一次取消；多选切换。
// 类型放这里而不是 SFC 里：`<script setup>` 不能导出模块成员，页面/测试都要能 import。
export interface FilterChipItem {
  value: string
  label: string
  count?: number
  title?: string
}

export type ChipValue = string | string[]

export function isChipSelected(value: string, current: ChipValue): boolean {
  return Array.isArray(current) ? current.includes(value) : current === value
}

export function nextChipValue(value: string, current: ChipValue, multiple = false): ChipValue {
  if (multiple) {
    const list = Array.isArray(current) ? current : current ? [current] : []
    return list.includes(value) ? list.filter((v) => v !== value) : [...list, value]
  }
  // 再点一次已选中的 chip = 取消该维度
  return isChipSelected(value, current) ? '' : value
}

// 带计数徽标的选项列表（值 → 计数）；没有计数的维度（value 为空）不显示徽标。
export function chipItems(
  options: Array<{ value: string; label: string }>,
  counts?: Record<string, number>,
): FilterChipItem[] {
  return options.map((opt) => ({
    value: opt.value,
    label: opt.label,
    count: counts && opt.value ? counts[opt.value] : undefined,
  }))
}
