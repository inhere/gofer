// 卡片「打开 seat」的直达（工作台 ?seat=<id>）：没人能收的 seat 定时消息卡只能跳到工作台，
// 名册里哪一行是它以前说不出来。现在卡片带座位 id，工作台把名册展到那一行、滚过去、高亮，
// 拿不准（已归档 / 已删）时给一条轻提示。
//
// 项目里没有 DOM 测试环境（只有 SSR），所以跳转目标用纯函数验，页面接线按仓库既有做法用源码断言。
import { describe, expect, it } from 'vitest'
import { itemLink } from '../utils/inbox'
import { cardLink } from '../utils/today'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const workbench = raw(import.meta.glob('./Workbench.vue', { eager: true, query: '?raw', import: 'default' }))
const sidebar = raw(
  import.meta.glob('../components/workbench/WorkbenchSidebar.vue', { eager: true, query: '?raw', import: 'default' }),
)
const panel = raw(
  import.meta.glob('../components/workbench/SeatRosterPanel.vue', { eager: true, query: '?raw', import: 'default' }),
)
const roster = raw(
  import.meta.glob('../components/workbench/SeatRoster.vue', { eager: true, query: '?raw', import: 'default' }),
)

describe('seat_schedule card', () => {
  it('opens the workbench on that seat', () => {
    expect(cardLink({ kind: 'seat_schedule', refs: { seat_id: 'seat-ab12', schedule_id: 'sch-1' } } as never))
      .toBe('/workbench?seat=seat-ab12')
    // 卡片没有座位 id（旧服务端）时退回工作台，不造一个假的定位
    expect(cardLink({ kind: 'seat_schedule', refs: {} } as never)).toBe('/workbench')
  })

  it('gives the inbox item the same deep link, encoded', () => {
    expect(itemLink({ kind: 'seat_schedule', refs: { seat_id: 'seat a/b' } } as never))
      .toBe('/workbench?seat=seat%20a%2Fb')
    expect(itemLink({ kind: 'seat_schedule', refs: {} } as never)).toBe('/workbench')
  })
})

describe('Workbench ?seat= deep link', () => {
  it('consumes the parameter, drops it from the URL and hands the id to the roster', () => {
    expect(workbench).toMatch(/function consumeRequestedSeat\(\): void \{[\s\S]*?route\.query\.seat[\s\S]*?seatTarget\.value = seatID/)
    // 座位可能在别的项目分组里：定位时把项目筛选一起放开，参数用完即去
    expect(workbench).toContain("key !== 'seat' && key !== 'project'")
    expect(workbench).toMatch(/void router\.replace\(\{ query: next \}\)\n\s*seatTarget\.value = seatID/)
    // 首次进入（布局就绪后）与导航到另一个 ?seat= 都要处理
    expect(workbench).toMatch(/consumeRequestedSeat\(\)\n\s*poller\.start\(\)/)
    expect(workbench).toMatch(/\(\) => route\.query\.seat,\n\s*\(\) => \{\n\s*if \(layoutReady\.value\) consumeRequestedSeat\(\)/)
    // 桌面侧栏与手机抽屉都接上（名册在两种布局里都渲染）
    expect(workbench.match(/:focus-seat="seatTarget"/g)).toHaveLength(2)
    expect(workbench.match(/@seat-located="onSeatLocated"/g)).toHaveLength(2)
  })

  it('says so once when the seat is gone, instead of failing silently', () => {
    expect(workbench).toContain("t('workbench.notice.seatMissing')")
    expect(workbench).toMatch(/function onSeatLocated\(result: \{ seatID: string; ok: boolean \}\): void \{[\s\S]*?if \(!result\.ok\) layoutNotice\.value/)
  })

  it('expands the roster, scrolls to the row and highlights it', () => {
    expect(roster).toContain('highlight?: string')
    expect(roster).toMatch(/open\.value = true\n\s*void nextTick\(\(\) => scrollToSeat\(focusID\.value\)\)/)
    expect(roster).toContain("scrollIntoView?.({ block: 'nearest' })")
    expect(roster).toContain("'seat-row--focus': focusID === seat.id")
    // 高亮与侧栏会话的选中行同一套样式
    expect(roster).toContain('.seat-row--focus .seat {')
  })

  it('decides found / missing on the loaded roster, not on a stale project-filtered list', () => {
    expect(sidebar).toContain(':focus-seat="focusSeat"')
    expect(sidebar).toContain("@located=\"emit('seatLocated', $event)\"")
    expect(panel).toContain("emit('located', { seatID, ok: true })")
    expect(panel).toContain("emit('located', { seatID, ok: false })")
    // 换筛选后旧列表不能用来下结论（否则被筛选挡住的座位会被错报成不存在）
    expect(panel).toContain("listProject.value !== (props.project || '')")
  })
})
