// 工作项健康度的展示文案（WorkDrawer 用）：正常不显示（空串）。文案走 i18n（util.workHealth.*）。
import type { WorkHealth } from '../api/types'
import { t, te } from '../i18n'

export function healthText(h: WorkHealth | string | undefined): string {
  if (!h || h === 'ok') return ''
  const key = `util.workHealth.${h}`
  return te(key) ? t(key) : ''
}
