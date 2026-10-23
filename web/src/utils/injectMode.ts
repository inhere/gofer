// injectMode：会话投递模式（auto / hold / force）的展示口径（2026-10-22 D5）。
// 只做展示与判定，投递本身在 server 侧；这里保证 web 与会话页说法一致。
import type { AgentSessionInjectMode } from '../api/types'
import { t } from '../i18n'

// INJECT_MODES 是投递模式的展示顺序。
export const INJECT_MODES: AgentSessionInjectMode[] = ['auto', 'hold', 'force']

// injectModeLabel 是按钮上的字（短）。
export function injectModeLabel(mode: AgentSessionInjectMode): string {
  switch (mode) {
    case 'hold':
      return t('sessions.inject.hold')
    case 'force':
      return t('sessions.inject.force')
    default:
      return t('sessions.inject.auto')
  }
}

// injectModeTitle 是悬停说明：选择它之后消息会怎样。
export function injectModeTitle(mode: AgentSessionInjectMode): string {
  switch (mode) {
    case 'hold':
      return t('sessions.inject.title.hold')
    case 'force':
      return t('sessions.inject.title.force')
    default:
      return t('sessions.inject.title.auto')
  }
}

// heldLabel 是「现在送」按钮上的字：没有等待投递的消息时返回空串（按钮不显示）。
export function heldLabel(pending: number | undefined): string {
  const n = Math.max(0, Math.trunc(pending ?? 0))
  return n > 0 ? t('sessions.inject.sendNow', { n }) : ''
}
