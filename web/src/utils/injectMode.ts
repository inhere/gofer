// injectMode：会话投递模式（auto / hold / force）的展示口径（2026-10-22 D5）。
// 只做展示与判定，投递本身在 server 侧；这里保证 web 与会话页说法一致。
import type { AgentSessionInjectMode } from '../api/types'

// INJECT_MODES 是投递模式的展示顺序。
export const INJECT_MODES: AgentSessionInjectMode[] = ['auto', 'hold', 'force']

// injectModeLabel 是按钮上的字（短）。
export function injectModeLabel(mode: AgentSessionInjectMode): string {
  switch (mode) {
    case 'hold':
      return '留待'
    case 'force':
      return '直送'
    default:
      return '看输入框'
  }
}

// injectModeTitle 是悬停说明：选择它之后消息会怎样。
export function injectModeTitle(mode: AgentSessionInjectMode): string {
  switch (mode) {
    case 'hold':
      return 'hold：从不往终端打字，消息一律留待，由会话下一次 hook 注入'
    case 'force':
      return 'force：不检查输入框直接粘贴（只在确定那个终端没人用时选）'
    default:
      return 'auto：只在输入框确认为空时打字；有人在打字或停在选择界面时消息留待'
  }
}

// heldLabel 是「现在送」按钮上的字：没有等待投递的消息时返回空串（按钮不显示）。
export function heldLabel(pending: number | undefined): string {
  const n = Math.max(0, Math.trunc(pending ?? 0))
  return n > 0 ? `现在送 ${n}` : ''
}
