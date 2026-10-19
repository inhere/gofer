// 信箱页的「接线」断言（gofer 信箱 M7）：纯逻辑在 utils/mail.ts 里单测，这里只钉住
// 路由 / 导航 / 页面到 client 与 util 的连接、Today 卡的跳转，以及抽屉里的信件入口——
// 也就是那些一改就会「页面在，但打不开或点不动」的地方。
import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(
  [
    '../router.ts',
    '../utils/nav.ts',
    '../api/client.ts',
    '../api/today.ts',
    '../utils/today.ts',
    '../views/Mail.vue',
    '../views/mailWiring.test.ts',
    '../components/SessionDrawer.vue',
    '../components/today/DecisionCard.vue',
  ],
  { eager: true, query: '?raw', import: 'default' },
) as Record<string, string>
const src = (n: string): string => {
  const k = Object.keys(mods).find((x) => x.endsWith(n))
  if (!k) throw new Error(`missing ${n}`)
  return mods[k]
}

describe('mailbox page wiring', () => {
  it('is a route and a nav entry', () => {
    const r = src('router.ts')
    expect(r).toContain("path: '/mail'")
    expect(r).toContain("import('./views/Mail.vue')")
    // 信箱不再占主导航：它只在抽屉的完整分组列表（drawerNavGroups）里还是导航项，
    // 顶栏上是「随手记」旁边的信封按钮（components/MailButton.vue）。
    expect(src('nav.ts')).toContain("{ to: '/mail', label: '信箱' }")
  })

  it('the page talks to /v1/mail through the client', () => {
    const page = src('Mail.vue')
    for (const fn of ['listMail(', 'sendMail(', 'replyMail(', 'ackMail(', 'cancelMail(', 'readdressMail(']) {
      expect(page).toContain(fn)
    }
    const c = src('client.ts')
    expect(c).toContain("`/v1/mail${qs ? `?${qs}` : ''}`")
    expect(c).toContain('`/v1/mail/${id}/reply`')
    expect(c).toContain('`/v1/mail/${id}/ack`')
    expect(c).toContain('`/v1/mail/${id}/cancel`')
    expect(c).toContain('`/v1/mail/${id}/readdress`')
  })

  it('keeps every rule in utils/mail.ts and renders the body as plain text', () => {
    const page = src('Mail.vue')
    for (const fn of [
      'mailFilterFromQuery(',
      'mailListQuery(',
      'groupMailThreads(',
      'mailStateLabel(',
      'mailStateTone(',
      'mailPartyLabel(',
      'mailExcerpt(',
      'mailReplyTarget(',
      'mailOutgoing(',
      'buildMailSend(',
      'buildMailReply(',
      'buildMailReaddress(',
    ]) {
      expect(page).toContain(fn)
    }
    // 正文来自别的 agent 会话，一律纯文本插值，绝不 v-html。
    expect(page).toContain('{{ m.body }}')
    expect(page).not.toContain('v-html')
    // 颜色走 token 变量，不写死色值。
    expect(page).toContain('var(--phosphor)')
    expect(page).toContain('@media (max-width: 900px)')
  })

  it('filter tabs, held mail actions and the compose form are on the page', () => {
    const page = src('Mail.vue')
    for (const t of [
      '`mail-tab-${f.key}`',
      'data-test="mail-to"',
      'data-test="mail-urgent"',
      'data-test="mail-reply-body"',
      'data-test="mail-readdress"',
      'data-test="mail-cancel"',
      'data-test="mail-held"',
    ]) {
      expect(page).toContain(t)
    }
    // 页签切回地址栏（可分享 / 供 Today 与抽屉深链复用）。
    expect(page).toContain('router.replace({ path: \'/mail\'')
  })
})

describe('the Today held-mail card navigates to the mailbox', () => {
  it('is a known today kind and links to the 待指派 tab', () => {
    expect(src('api/today.ts')).toContain("| 'mail_held'")
    const util = src('utils/today.ts')
    expect(util).toContain("case 'mail_held':")
    expect(util).toContain('return MAIL_HELD_LINK')
    // 只有跳转，没有写接口（卡片动作 open 是本地跳转）。
    const writeSection = util.slice(util.indexOf('export function runCardAction'))
    expect(writeSection).not.toContain('mail_held')
  })

  it('the card renders through the shared decision card', () => {
    const c = src('DecisionCard.vue')
    expect(c).toContain('actionLink(props.card, a)')
    expect(c).toContain('cardLink(props.card)')
  })
})

describe('the session drawer links to the session mailbox', () => {
  it('shows the letter count of the session and links to /mail?session=', () => {
    const d = src('SessionDrawer.vue')
    expect(d).toContain('listMail({ session: props.sid, box: \'all\'')
    expect(d).toContain('data-test="session-mail"')
    expect(d).toContain('`/mail?session=${encodeURIComponent(session.session_id)}`')
  })
})

describe('a held letter links to its capture item', () => {
  it('opens the capture box item, where candidates, new job and drop live', () => {
    const v = src('Mail.vue')
    expect(v).toContain('data-test="mail-held-item"')
    expect(v).toContain("query: { id: m.capture_item_id }")
  })
})
