// 状态族的图标（设计稿 §5）：形状 + 颜色一起编码，色弱与灰度也能分辨，小尺寸下也认得出来。
//
// 这里只描述「画什么」；颜色走 tokens.css 的族变量（.k-<族> 给 --c / --soft），
// 具体渲染在 StatusGlyph.vue。测试遍历这张表，保证 7 个族都有各自的图标。
import { Circle, CircleAlert, CircleCheck, CircleDashed, CircleX, Clock, LoaderCircle, type LucideIcon } from '@lucide/vue'
import type { StatusFamily } from '../utils/statusFamily'

// kind 与 lucide 图标同名：既是 data-kind 的稳定值，也直接说明画的是哪一个图标。
export type GlyphKind =
  | 'loader-circle' // 运行中：旋转的缺口圆环
  | 'circle-alert' // 需要人：圆内感叹号
  | 'circle-check' // 完成：圆内对勾
  | 'circle-x' // 失败：圆内叉
  | 'clock' // 超时：时钟
  | 'circle' // 空闲：空心圆
  | 'circle-dashed' // 排队：虚线圆

export interface GlyphSpec {
  kind: GlyphKind
  icon: LucideIcon
  // 形状的描述（无障碍名称的备选文案 key）
  labelKey: string
  // 常驻旋转：只有运行中
  spin?: boolean
}

export const GLYPHS: Record<StatusFamily, GlyphSpec> = {
  run: { kind: 'loader-circle', icon: LoaderCircle, labelKey: 'status.family.run', spin: true },
  need: { kind: 'circle-alert', icon: CircleAlert, labelKey: 'status.family.need' },
  done: { kind: 'circle-check', icon: CircleCheck, labelKey: 'status.family.done' },
  fail: { kind: 'circle-x', icon: CircleX, labelKey: 'status.family.fail' },
  time: { kind: 'clock', icon: Clock, labelKey: 'status.family.time' },
  idle: { kind: 'circle', icon: Circle, labelKey: 'status.family.idle' },
  queue: { kind: 'circle-dashed', icon: CircleDashed, labelKey: 'status.family.queue' },
}

export function glyphSpec(family: StatusFamily): GlyphSpec {
  return GLYPHS[family]
}
