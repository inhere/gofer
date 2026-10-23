// 页面写操作的灰显判定（gofer-bp9c）：与今天页（utils/todayGate.ts）同一套 whoami 口径，
// 权限名对齐 internal/httpapi/authz_routes.go 里该动作真正调用的那条路由的声明：
//   - 只读上限（ceiling.read_only）：所有写操作一律灰显，理由用 ceiling 的统一说法；
//   - InHandler(permDeny, perm)：资源属于本人时不要权限（ownerId === 本人 caller_id），
//     是别人的资源且缺 perm 时灰显，理由写明缺哪个权限；
//   - Public 路由（work item、tracker issue 的读写）：不要权限，只受只读上限约束；
//   - 一条路由声明了几种可选规则时（信箱的 session.reply.others / job.manage.others），
//     用 anyPerms（任一持有即可）。
// 纯查看不受影响。whoami 没取到（旧服务端 / 网络）时一律放行，让服务端的 403 说话。
import { CEILING_READ_ONLY_NOTICE, ceilingReadOnly } from './ceiling'
import { holdsPermission } from './permissionGate'
import type { AuthWhoami } from './permissions'
import { t } from '../i18n'

// 缺权限时按钮 title 的统一说法（灰显原因）。
export function missingPermissionText(perm: string): string {
  return t('util.perm.missing', { perm })
}

export interface WriteSpec {
  // 全部持有才放行（路由上的单一权限 / 同时要求的几条）。
  perms?: readonly string[]
  // 任一持有即可（路由声明里列了几种可选规则）。
  anyPerms?: readonly string[]
  // 资源归属（plan.owner、信件的发件方等）：等于本人时不需要权限。
  ownerId?: string
}

// pageWriteBlock：这个写操作能不能点。返回空串 = 可以；否则是灰显原因（挂到 title）。
export function pageWriteBlock(who: AuthWhoami | null | undefined, spec: WriteSpec = {}): string {
  if (!who) return ''
  if (ceilingReadOnly(who.ceiling)) return CEILING_READ_ONLY_NOTICE
  // 凭据不受权限管辖（worker / job 凭据等）时后端另有闸门，界面不隐藏。
  if (who.kind !== 'user') return ''
  if (spec.ownerId && spec.ownerId === who.caller_id) return ''
  const perms = spec.perms ?? []
  const missing = perms.filter((p) => !holdsPermission(who, p))
  if (missing.length > 0) return missingPermissionText(missing[0])
  const any = spec.anyPerms ?? []
  if (any.length > 0 && !any.some((p) => holdsPermission(who, p))) {
    return missingPermissionText(any.join(t('util.perm.orJoin')))
  }
  return ''
}
