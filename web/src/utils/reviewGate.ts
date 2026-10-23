// 审阅（验收 / 批准）入口的显示规则（gofer-7798 第二期 E4）：界面只决定「摆不摆按钮」，
// 判定与服务端 403 的口径一致（服务端仍是唯一权威）：
//   - 只读上限（read_only）：任何写操作都不摆，验收 / 批准同样不摆；
//   - 需要 job.review；审阅**别人**提交的 job 还要 job.review.others（自己的交付只要 job.review）；
//   - 受限调用方（whoami 带 ceiling）不能审阅「上限所有者是自己」的 job——机器人不给自己签字。
// 服务端按 job.OriginCaller 判上限所有者；详情接口不下发 origin caller，所以这里退回按提交者
// （job.caller_id）判断，两者在「自己提交、自己审」这条路上等价。
// whoami 取不到（旧 server / 网络）时一律放行，让服务端的 403 说话。
import { t } from '../i18n'
import { ceilingReadOnly } from './ceiling'
import { holdsPermission } from './permissionGate'
import type { AuthWhoami } from './permissions'

// 被审阅的 job 只需要这几个字段（Job 结构里的可选子集）。
export interface ReviewTarget {
  // 提交者（上限所有者）。
  caller_id?: string
}

export interface ReviewGate {
  // 显示按钮吗。
  allowed: boolean
  // allowed = false 时在原位置给出的灰字原因；allowed = true 时为空串。
  reason: string
}

export const REVIEW_PERM = 'job.review'
export const REVIEW_OTHERS_PERM = 'job.review.others'

const ALLOWED: ReviewGate = { allowed: true, reason: '' }

function deny(reason: string): ReviewGate {
  return { allowed: false, reason }
}

// canReviewJob：验收 / 批准按钮显示吗，不显示时给出原因。
export function canReviewJob(
  who: AuthWhoami | null | undefined,
  job: ReviewTarget | null | undefined,
): ReviewGate {
  if (!who) return ALLOWED
  if (ceilingReadOnly(who.ceiling)) {
    return deny(t('util.review.readOnly'))
  }
  // 凭据不受权限管辖（worker / job 凭据等）时后端另有闸门，界面不隐藏。
  if (who.kind === 'user') {
    if (!holdsPermission(who, REVIEW_PERM)) {
      return deny(t('util.review.noPerm'))
    }
    const others = !!job?.caller_id && job.caller_id !== who.caller_id
    if (others && !holdsPermission(who, REVIEW_OTHERS_PERM)) {
      return deny(t('util.review.noOthersPerm'))
    }
  }
  // 受限调用方不能给自己提交的 job 签字（服务端同口径）。
  if (who.ceiling && job?.caller_id && job.caller_id === who.caller_id) {
    return deny(t('util.review.selfCeiling'))
  }
  return ALLOWED
}
