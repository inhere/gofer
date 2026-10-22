// 页面写操作的灰显判定（gofer-bp9c）：只读身份一律灰显并给出只读理由；写身份按该动作
// 调用的路由声明判权限（Require 的权限 / InHandler 的 *.others 只看别人的资源 / Public 不要权限）；
// 有权限时返回空串（按钮可点）；whoami 未知时放行，让服务端的 403 说话。
import { describe, expect, it } from 'vitest'
import { CEILING_READ_ONLY_NOTICE } from './ceiling'
import { missingPermissionText, pageWriteBlock } from './pageGate'
import type { AuthWhoami } from './permissions'

function who(over: Partial<AuthWhoami>): AuthWhoami {
  return { caller_id: 'alice', kind: 'user', root: false, permissions: [], grants_version: 1, ...over }
}
const holds = (...names: string[]) => names.map((name) => ({ name, source: 'db' }))
const readOnlyWho = who({ ceiling: { allow_exec: false, global_memory: false, read_only: true } })

describe('a read-only identity cannot write anything', () => {
  it('grey every kind of write spec with the read-only reason', () => {
    expect(pageWriteBlock(readOnlyWho, { perms: ['plan.manage.others'] })).toBe(CEILING_READ_ONLY_NOTICE)
    expect(pageWriteBlock(readOnlyWho, { anyPerms: ['session.reply.others', 'job.manage.others'] })).toBe(
      CEILING_READ_ONLY_NOTICE,
    )
    // 自己的资源也一样写不了。
    expect(pageWriteBlock(readOnlyWho, { perms: ['plan.manage.others'], ownerId: 'alice' })).toBe(
      CEILING_READ_ONLY_NOTICE,
    )
    // Public 路由（work item / tracker issue 写）同样灰显。
    expect(pageWriteBlock(readOnlyWho)).toBe(CEILING_READ_ONLY_NOTICE)
  })
  it('a root administrator with a read-only ceiling is still greyed', () => {
    expect(pageWriteBlock(who({ root: true, ceiling: readOnlyWho.ceiling }), { perms: ['plan.manage.others'] })).toBe(
      CEILING_READ_ONLY_NOTICE,
    )
  })
})

describe('a writable identity is judged by the route it calls', () => {
  it('a plain permission is required, and holding it unblocks the button', () => {
    expect(pageWriteBlock(who({}), { perms: ['plan.manage.others'], ownerId: 'bob' })).toBe(
      missingPermissionText('plan.manage.others'),
    )
    expect(pageWriteBlock(who({ permissions: holds('plan.manage.others') }), { perms: ['plan.manage.others'], ownerId: 'bob' })).toBe('')
  })

  it('the *.others rule leaves your own resource alone (InHandler permDeny)', () => {
    expect(pageWriteBlock(who({}), { perms: ['plan.manage.others'], ownerId: 'alice' })).toBe('')
    // owner 未知（老服务端）时不拦，交给服务端判。
    expect(pageWriteBlock(who({}), { perms: ['plan.manage.others'] })).toBe(
      missingPermissionText('plan.manage.others'),
    )
  })

  it('anyPerms needs one of the alternatives (mail reply: session.reply.others / job.manage.others)', () => {
    const spec = { anyPerms: ['session.reply.others', 'job.manage.others'] }
    expect(pageWriteBlock(who({}), spec)).toBe(missingPermissionText('session.reply.others 或 job.manage.others'))
    expect(pageWriteBlock(who({ permissions: holds('session.reply.others') }), spec)).toBe('')
    expect(pageWriteBlock(who({ permissions: holds('job.manage.others') }), spec)).toBe('')
  })

  it('public routes (work item / tracker issue writes) need no permission at all', () => {
    expect(pageWriteBlock(who({}))).toBe('')
  })

  it('a root administrator may do everything', () => {
    expect(pageWriteBlock(who({ root: true }), { perms: ['plan.manage.others'], ownerId: 'bob' })).toBe('')
  })
})

describe('unknown identity hides nothing', () => {
  it('whoami not loaded yet, or a credential outside the permission subjects', () => {
    expect(pageWriteBlock(null, { perms: ['plan.manage.others'] })).toBe('')
    expect(pageWriteBlock(undefined)).toBe('')
    expect(pageWriteBlock(who({ kind: 'worker' }), { perms: ['plan.manage.others'], ownerId: 'bob' })).toBe('')
  })
})
