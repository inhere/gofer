// 统一交互卡片（InteractionCard.vue）的纯逻辑：推荐项默认选中、勾选后的提交值、只读回显。
// gofer_ask 的选项 value 与 label 相同；多选提交的是选中 label 的 JSON 数组（见
// internal/job/interaction_ext.go 的答案校验），单选提交 label（填了「其他」时以它为准）。
import type { Interaction, InteractionOption } from '../api/types'

// 选项的展示名：label 优先，回退 value（与卡片的按钮文案同一口径）。
export function optionLabel(opt: InteractionOption): string {
  return opt.label ?? opt.value
}

// 推荐项：recommended 里可能写的是 label 也可能是 value，两者都认。
export function isRecommended(
  it: Pick<Interaction, 'recommended'>,
  opt: InteractionOption,
): boolean {
  const want = (it.recommended ?? '').trim()
  if (want === '') {
    return false
  }
  return want === opt.value || want === (opt.label ?? '').trim()
}

// 初始勾选：推荐项默认选中（多选也一样，人可以再改）。没有推荐项时为空 —— 不替人预选。
export function initialPicks(it: Interaction): string[] {
  const rec = (it.options ?? []).find((o) => isRecommended(it, o))
  return rec ? [rec.value] : []
}

// 勾选后的提交值：多选是选中 label 的 JSON 数组（「其他」的文本作为其中一项），
// 单选是所选 label 或「其他」文本（填了以它为准，与后端校验一致）；null = 还没选、不许提交。
export function choiceAnswer(it: Interaction, picks: string[], other: string): string | null {
  const free = other.trim()
  const labels = picks
    .map((v) => (it.options ?? []).find((o) => o.value === v))
    .filter((o): o is InteractionOption => o !== undefined)
    .map((o) => optionLabel(o))
  if (it.multi) {
    const out = free === '' ? labels : [...labels, free]
    return out.length === 0 ? null : JSON.stringify(out)
  }
  if (free !== '') {
    return free
  }
  return labels.length === 0 ? null : labels[0]
}

// 只读回显：多选的 JSON 数组转成逗号分隔（人话），其余原样（含老卡片存的纯文本）。
export function interactionAnswerText(it: Interaction): string {
  const raw = (it.answer ?? '').trim()
  if (raw === '' || !it.multi) {
    return raw
  }
  try {
    const picked = JSON.parse(raw) as unknown
    if (Array.isArray(picked)) {
      return picked.filter((v): v is string => typeof v === 'string').join(', ')
    }
  } catch {
    // 不是 JSON 就按原文显示（人工代答可能写的是自由文本）。
  }
  return raw
}
