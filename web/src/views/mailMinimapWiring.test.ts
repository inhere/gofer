// 信箱对话小地图的「接线」断言：位置计算与点击跳转在 utils/mailMinimap.ts 里单测
// （mailMinimap.test.ts），这里只钉住「页面真的挂了小地图、跳转真的滚了时间线、整页真的
// 占满视口」——也就是一改就会「地图在但不动 / 页面又出现外层滚动条」的地方。
import { describe, expect, it } from 'vitest'

const mods = import.meta.glob(['../views/Mail.vue', '../components/mail/MailMinimap.vue', '../ui/AppShell.vue'], {
  eager: true,
  query: '?raw',
  import: 'default',
}) as Record<string, string>
const src = (n: string): string => {
  const k = Object.keys(mods).find((x) => x.endsWith(n))
  if (!k) throw new Error(`missing ${n}`)
  return mods[k]
}

describe('the conversation minimap is on the mailbox page', () => {
  it('renders one mark per letter beside the timeline', () => {
    const page = src('Mail.vue')
    expect(page).toContain("import MailMinimap from '../components/mail/MailMinimap.vue'")
    expect(page).toContain('<MailMinimap')
    // 位置 / 颜色 / 悬停文案 / 可视范围都来自 util，组件里不再各算一遍。
    for (const fn of ['buildMinimapMarks(', 'minimapBand(', 'minimapVisible(', 'minimapScrollTopForPct(']) {
      expect(page).toContain(fn)
    }
    const minimap = src('MailMinimap.vue')
    expect(minimap).toContain('data-test="mail-minimap"')
    expect(minimap).toContain('data-test="mail-minimap-band"')
    expect(minimap).toContain(':data-test="`mail-minimap-mark-${mk.id}`"')
    // 悬停给出「发件方与时间 + 首行摘要」，点击把标记派发出去。
    expect(minimap).toContain(':title="mk.tooltip"')
    expect(minimap).toContain("@click=\"emit('jump', mk)\"")
    // 手机宽度下隐藏（时间线已占满宽度；断点是设计稿冻结的 760，§4.3）。
    expect(minimap).toContain('@media (max-width: 760px)')
  })

  it('clicking a mark scrolls the timeline to that letter', () => {
    const page = src('Mail.vue')
    // 时间线是页面自己的滚动容器：滚动量从这个容器量，点击后滚到标记对应的位置。
    expect(page).toContain('ref="timelineEl"')
    expect(page).toContain('@scroll="syncTimelineScroll"')
    expect(page).toContain('minimapScrollTopForPct(mark.topPct, el.clientHeight, el.scrollHeight)')
    expect(page).toContain('scrollToLetter(m.reply_to)')
    expect(page).toContain('@jump="(mk) => jumpToMark(mk.id)"')
  })
})

describe('the mailbox page fills the viewport', () => {
  it('locks the shell height and scrolls inside the two panes', () => {
    const app = src('AppShell.vue')
    // 顶栏以下的高度锁在视口里（外层不出滚动条），滚动交给页内列表 / 时间线。
    expect(app).toContain("'shell--fill': route.path === '/mail'")
    expect(app).toContain("'content--fill': route.path === '/mail'")
    expect(app).toContain('.shell--fill {')

    const page = src('Mail.vue')
    // 左列表与右时间线都是各自滚动的弹性列，不再写死 max-height: 70vh / 62vh。
    expect(page).not.toContain('max-height: 70vh')
    expect(page).not.toContain('max-height: 62vh')
    expect(page).toContain('.timeline-wrap {')
    // 气泡用对话区的大部分宽度，但用 ch 限住超宽屏上的行长。
    expect(page).toContain('max-width: min(94%, 96ch)')
  })
})
