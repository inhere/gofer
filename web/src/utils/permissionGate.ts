// 全站「按权限隐藏操作」：一张小表把界面动作映射到权限名（权限名见 docs/reference/permissions.md），
// 判定只看 whoami。服务端仍是唯一权威——这里只是别把用户注定被拒的按钮摆出来；
// 还没取到 whoami、或凭据不是个人账号（权限不管它）时一律不隐藏，让服务端的 403 说话。
import { AUTH_MANAGE } from './permissions'
import type { AuthWhoami } from './permissions'
import { ceilingReadOnly } from './ceiling'
import { t } from '../i18n'

export interface GateRule {
  // 动作要求的权限（全部持有才显示）。
  perms: string[]
  // 只管「别人的」资源：资源属于自己时不需要这些权限（job.delete.others 一类 *.others 权限）。
  othersOnly?: boolean
  // 给人看的名字（403 提示与悬停说明用）。
  label: string
}

// label 是 getter：文案随当前语言取（i18n 字典 util.perm.gate.*）。
export const ACTION_PERMISSIONS = {
  // 授权管理：新增 / 撤销授权、会话「授权」按钮。
  'grant.manage': { perms: [AUTH_MANAGE], get label() { return t('util.perm.gate.grant') } },
  // Runners：用 server 二进制升级 worker；注册 / 删除 / 重载 worker。
  'worker.upgrade': { perms: ['worker.upgrade'], get label() { return t('util.perm.gate.workerUpgrade') } },
  'worker.manage': { perms: ['worker.manage'], get label() { return t('util.perm.gate.workerManage') } },
  // 设置 → 关于：一键更新。
  'update.apply': { perms: ['update.apply'], get label() { return t('util.perm.gate.updateApply') } },
  // 配置：写配置 / 校验 / 重载；项目增删改。
  'config.write': { perms: ['config.write'], get label() { return t('util.perm.gate.configWrite') } },
  // 设置 → 扩展设置：数据库里的阈值 / 名单 / 提示词（settings.write，中风险，默认不种）。
  'settings.write': { perms: ['settings.write'], get label() { return t('util.perm.gate.settingsWrite') } },
  'project.write': { perms: ['project.write'], get label() { return t('util.perm.gate.projectWrite') } },
  // job：删除记录（他人的需要 job.delete.others，自己的不需要）。
  'job.delete': { perms: ['job.delete.others'], othersOnly: true, get label() { return t('util.perm.gate.jobDelete') } },
  // 隧道：托管转发 / 预设（tunnel.hosted）；停别人的转发（tunnel.manage.others）。
  'tunnel.hosted': { perms: ['tunnel.hosted'], get label() { return t('util.perm.gate.tunnelHosted') } },
  'tunnel.stopOthers': { perms: ['tunnel.manage.others'], othersOnly: true, get label() { return t('util.perm.gate.tunnelStopOthers') } },
} satisfies Record<string, GateRule>

export type GateAction = keyof typeof ACTION_PERMISSIONS

// 持有某项权限：根管理员全有；whoami 未知或凭据不受权限管辖时不拦。
export function holdsPermission(who: AuthWhoami | null | undefined, perm: string): boolean {
  if (!who) return true
  if (who.kind !== 'user') return true
  if (who.root) return true
  return who.permissions.some((p) => p.name === perm)
}

// 调用方上限是只读时，下面的动作表里的每一条都是写操作——一律不显示（服务端也会 403）。
export function writeBlocked(who: AuthWhoami | null | undefined): boolean {
  return ceilingReadOnly(who?.ceiling)
}

// 动作要显示吗？ownerId 是资源所属调用方（只对 othersOnly 的动作有意义）。
// 只读上限（read_only）先于权限：它收窄的正是写动作，表里没有一条读动作。
export function canDo(who: AuthWhoami | null | undefined, action: GateAction, ownerId?: string): boolean {
  if (writeBlocked(who)) return false
  const rule: GateRule = ACTION_PERMISSIONS[action]
  if (rule.othersOnly && who && ownerId && ownerId === who.caller_id) return true
  return rule.perms.every((p) => holdsPermission(who, p))
}

// 缺哪些权限（悬停提示 / 只读说明用）。
export function missingFor(who: AuthWhoami | null | undefined, action: GateAction): string[] {
  const rule: GateRule = ACTION_PERMISSIONS[action]
  return rule.perms.filter((p) => !holdsPermission(who, p))
}
