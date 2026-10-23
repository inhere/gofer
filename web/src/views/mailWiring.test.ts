// 信箱页的「接线」断言（gofer 信箱 M7）：纯逻辑在 utils/mail.ts 里单测，这里只钉住
// 路由 / 导航 / 页面到 client 与 util 的连接、Today 卡的跳转，以及抽屉里的信件入口——
// 也就是那些一改就会「页面在，但打不开或点不动」的地方。
import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(
  [
    '../router.routes.ts',
    '../utils/shell.ts',
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
    const r = src('router.routes.ts')
    expect(r).toContain("path: '/mail'")
    expect(r).toContain("import('./views/Mail.vue')")
    // 信箱在侧栏「协作」分组里是导航项（utils/shell.ts），顶栏另有信封按钮（components/MailButton.vue）。
    expect(src('shell.ts')).toContain("{ to: '/mail', key: 'nav.mail', glyph: '✉' }")
  })

  it('the page talks to /v1/mail through the client', () => {
    const page = src('Mail.vue')
    for (const fn of ['listMail(', 'listMailConversations(', 'sendMail(', 'replyMail(', 'ackMail(', 'cancelMail(', 'readdressMail(']) {
      expect(page).toContain(fn)
    }
    const c = src('client.ts')
    expect(c).toContain("`/v1/mail${qs ? `?${qs}` : ''}`")
    expect(c).toContain('`/v1/mail/${id}/reply`')
    expect(c).toContain('`/v1/mail/${id}/ack`')
    expect(c).toContain('`/v1/mail/${id}/cancel`')
    expect(c).toContain('`/v1/mail/${id}/readdress`')
  })

  it('keeps every rule in utils/mail.ts and renders the body through the shared markdown block', () => {
    const page = src('Mail.vue')
    for (const fn of [
      'mailFilterFromQuery(',
      'mailListQuery(',
      'groupMailConversations(',
      'mailStateTone(',
      'mailPartyLabel(',
      'mailSummary(',
      'replyTarget(',
      'mailOutgoing(',
      'buildMailSend(',
      'buildMailReply(',
      'buildMailReaddress(',
    ]) {
      expect(page).toContain(fn)
    }
    // 正文是别的 agent 会话写的 markdown：只走 MarkdownBlock（marked + DOMPurify 消毒），
    // 页面自己不拼 HTML（design 2026-10-20「三、markdown 渲染」）。
    expect(page).toContain('<MarkdownBlock :text="bodyText(m)" />')
    expect(page).not.toContain('v-html')
    // 颜色走 token 变量，不写死色值；手机断点是设计稿冻结的 760（§4.3）。
    expect(page).toContain('var(--accent)')
    expect(page).toContain('@media (max-width: 760px)')
  })

  it('filter chips, held mail actions and the compose form are on the page', () => {
    const page = src('Mail.vue')
    for (const t of [
      '<FilterChips',
      'data-test="mail-to"',
      'data-test="mail-readdress"',
      'data-test="mail-cancel"',
      'data-test="mail-held"',
    ]) {
      expect(page).toContain(t)
    }
    // 页签切回地址栏（可分享 / 供 Today 与抽屉深链复用）。
    expect(page).toContain('router.replace({ path: \'/mail\'')
    // 书信正文与回复都走统一的 Composer（急件开关由调用方声明）
    expect(page.match(/<Composer/g)?.length).toBe(2)
    expect(page.match(/urgent-option/g)?.length).toBe(2)
  })

  // 写信框的收件人（gofer-ndy5，设计 2026-10-22「D5」）：会话多选 + 「按项目 / 主题 / agent
  // 全部」选器 + 可删除标签 + 手写地址（高级）+ 服务端投递预览。
  it('the compose form picks sessions, scopes and previews the delivery count', () => {
    const page = src('Mail.vue')
    for (const t of [
      'data-test="mail-to-tags"',
      'data-test="mail-to-tag"',
      'data-test="mail-session-search"',
      'data-test="mail-session-options"',
      'data-test="mail-scope-scheme"',
      'data-test="mail-scope-value"',
      'data-test="mail-scope-add"',
      'data-test="mail-to-preview"',
      'data-test="mail-preview-error"',
      'mailComposeAddresses(',
      'toggleComposeSession(',
      'removeComposeAddress(',
      'addComposeScope(',
    ]) {
      expect(page).toContain(t)
    }
    // 投递数预览走本页字典的 previewText（数字来自服务端的 dry_run 或本地估算）
    expect(page).toContain('previewText(')
    expect(page).toContain('mailRecipientPreview(')
    // 预览是服务端的 dry_run（同一个 POST /v1/mail，不新增路由；G056）。
    expect(page).toContain('previewMail(')
    expect(src('client.ts')).toContain('dry_run: true')
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
