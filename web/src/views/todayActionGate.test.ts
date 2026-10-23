// 今天页写按钮的接线断言（gofer-darr）：卡片动作走 utils/todayGate 的判定、只读身份时
// 「专注处理」灰显并说明原因。纯判定见 utils/todayGate.test.ts，这里只确认页面真的用了它。
import { describe, expect, it } from 'vitest'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const card = raw(import.meta.glob('../components/today/DecisionCard.vue', { eager: true, query: '?raw', import: 'default' }))
const queue = raw(import.meta.glob('../components/today/DecisionQueue.vue', { eager: true, query: '?raw', import: 'default' }))
const today = raw(import.meta.glob('./Today.vue', { eager: true, query: '?raw', import: 'default' }))

describe('今天页：卡片写操作按权限灰显', () => {
  it('决策卡用 whoami 与 todayGate 判定每个动作', () => {
    expect(card).toContain("import { useWhoami } from '../../store/whoami'")
    expect(card).toContain("import { cardActionBlock } from '../../utils/todayGate'")
    expect(card).toContain('cardActionBlock(whoami.value, props.card, a)')
  })
  it('动作按钮与「按建议」按钮灰显时挂 title 说明原因', () => {
    expect(card).toContain(':disabled="!!actionBlock(a)"')
    expect(card).toContain(':title="actionBlock(a) || undefined"')
    expect(card).toContain(':disabled="!!actionBlock(advice)"')
    expect(card).toContain(':title="actionBlock(advice) || undefined"')
  })
  it('灰显的动作不发请求（专注模式的数字键 / a / r 也走同一条路）', () => {
    expect(card).toContain('if (actionBlock(a)) return')
  })
  it('建议的采纳 / 忽略在只读身份下也灰显（这两条路由是 Public，只被只读上限拦）', () => {
    expect(card).toContain('if (readOnly.value) return')
    expect(card).toContain(':disabled="readOnly"')
    expect(card).toContain(':title="readOnly ? CEILING_READ_ONLY_NOTICE : undefined"')
  })
  it('纯查看的链接（看 diff / 打开 plan）不受影响', () => {
    expect(card).toContain("v-if=\"a.id === 'diff' || a.id === 'open'\"")
  })
})

describe('今天页：只读身份下「专注处理」灰显', () => {
  it('页头按钮取 whoami 的 readOnly 并说明原因', () => {
    expect(today).toContain('const { readOnly } = useWhoami()')
    // 类型 / 项目 chip 过滤后的张数决定「专注处理」能不能点
    expect(today).toContain(':disabled="!shownCards.length || readOnly"')
    expect(today).toContain(':title="readOnly ? CEILING_READ_ONLY_NOTICE : undefined"')
  })
  it('队列底部的「专注处理」同样灰显', () => {
    expect(queue).toContain('const { readOnly } = useWhoami()')
    expect(queue).toContain(':disabled="readOnly"')
    expect(queue).toContain(':title="readOnly ? CEILING_READ_ONLY_NOTICE : undefined"')
  })
})
