import type { WorkbenchStatus } from '../../api/types'
import { t } from '../../i18n'
import { statusFamily, type StatusFamily } from '../../utils/statusFamily'

// 会话状态展示：文字走 i18n（status.<值>），形状与颜色走状态族（设计稿 §5），
// 替换原来的中文 label + tone 圆点两套表达。
export function threadStatusPresentation(status: WorkbenchStatus): { label: string; family: StatusFamily } {
  return { label: t(`status.${status}`), family: statusFamily(status) }
}
