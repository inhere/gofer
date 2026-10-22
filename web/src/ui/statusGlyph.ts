// 状态族的字形规格（设计稿 §5）：形状 + 颜色一起编码，色弱与灰度也能分辨。
//
// 这里只描述「画什么」；颜色走 tokens.css 的族变量（.k-<族> 给 --c / --soft），
// 具体 SVG 在 StatusGlyph.vue。测试遍历这张表，保证 7 个族都有字形。
import type { StatusFamily } from '../utils/statusFamily'

export type GlyphKind =
  | 'dot-pulse' // 实心圆 + 脉冲环：运行
  | 'diamond' // 菱形：需要人
  | 'check' // 圆加勾：完成
  | 'cross' // 圆角方块加叉：失败
  | 'clock' // 时钟：超时
  | 'ring' // 空心圆：空闲
  | 'dashed-ring' // 虚线圆：排队

export interface GlyphSpec {
  kind: GlyphKind
  // 形状的描述（无障碍名称的备选文案 key）
  labelKey: string
}

export const GLYPHS: Record<StatusFamily, GlyphSpec> = {
  run: { kind: 'dot-pulse', labelKey: 'status.family.run' },
  need: { kind: 'diamond', labelKey: 'status.family.need' },
  done: { kind: 'check', labelKey: 'status.family.done' },
  fail: { kind: 'cross', labelKey: 'status.family.fail' },
  time: { kind: 'clock', labelKey: 'status.family.time' },
  idle: { kind: 'ring', labelKey: 'status.family.idle' },
  queue: { kind: 'dashed-ring', labelKey: 'status.family.queue' },
}

export function glyphSpec(family: StatusFamily): GlyphSpec {
  return GLYPHS[family]
}
