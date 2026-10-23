// 今天页（N3）卡片上「会改状态」的按钮能不能点（gofer-darr）：只读身份、卡片对应的服务端
// 权限不符时灰显并给出原因；纯查看（详情 / 看 diff / 打开 plan）不受影响。
// 口径对齐 internal/httpapi/authz_routes.go——卡片动作各自调用现成的写接口：
//   interaction 卡：作答走 POST /v1/jobs/{id}/interactions/{iid}/answer，路由上是 Require(interaction.answer)；
//   review / approval 卡：验收 / 批准走 reviewGate（job.review，审别人提交的还要 job.review.others）；
//   memory 卡：采纳要 memory.write（Require），忽略是 Public；
//   relay 回复 / permission 卡作答 / work 卡的回复·请求汇报·搁置 / plan 卡继续：服务端按资源归属
//   在 handler 里判（InHandler，自己的资源不需要权限），界面判不了，只拦只读身份；
//   「稍后」是 ReadSafe 的个人偏好写，只读身份服务端照收，所以这里不拦。
// whoami 没取到（旧服务端 / 网络）时一律放行，让服务端的 403 说话（与 utils/permissionGate 同口径）。
import type { TodayAction, TodayCard } from '../api/today'
import { ceilingReadOnlyNotice, ceilingReadOnly } from './ceiling'
import { missingPermissionText } from './pageGate'
import { holdsPermission } from './permissionGate'
import type { AuthWhoami } from './permissions'
import { canReviewJob } from './reviewGate'

export const INTERACTION_ANSWER_PERM = 'interaction.answer'
export const MEMORY_WRITE_PERM = 'memory.write'

// 缺权限的统一说法与其它页面的写入口共用（utils/pageGate.ts）。
export { missingPermissionText }

// cardActionBlock：这一个卡片动作能不能点。返回空串 = 可以；否则是灰显原因（挂到 title）。
export function cardActionBlock(
  who: AuthWhoami | null | undefined,
  card: Pick<TodayCard, 'kind'> | null | undefined,
  action: Pick<TodayAction, 'id'> | null | undefined,
): string {
  if (!who || !card) return ''
  if (ceilingReadOnly(who.ceiling)) return ceilingReadOnlyNotice()
  if (who.kind !== 'user') return ''
  switch (card.kind) {
    case 'interaction':
      return holdsPermission(who, INTERACTION_ANSWER_PERM) ? '' : missingPermissionText(INTERACTION_ANSWER_PERM)
    case 'review':
    case 'approval':
      // 卡片 refs 里没有提交者（caller_id），这里只能判 job.review；*.others 与「不给自己签字」
      // 留给 job 详情页的同一条规则（utils/reviewGate）在服务端裁决。
      return canReviewJob(who, {}).reason
    case 'memory':
      return action?.id === 'dismiss' || holdsPermission(who, MEMORY_WRITE_PERM)
        ? ''
        : missingPermissionText(MEMORY_WRITE_PERM)
    default:
      return ''
  }
}
