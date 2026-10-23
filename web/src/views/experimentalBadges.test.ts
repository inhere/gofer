// 「实验性」标注落在哪些入口：docs/guide/stability.md 的实验性清单 → 控制台页面。
// 只读源码（?raw）钉住"入口处确实摆了 ExperimentalBadge"，不渲染页面——这些页面的数据
// 都来自 server，渲染断言在这里帮不上忙，反而脆。
import { describe, expect, it } from 'vitest'

const IMPORT = "import ExperimentalBadge from '../components/ExperimentalBadge.vue'"

const mods = import.meta.glob(
  [
    './Workflows.vue',
    './NewWorkflow.vue',
    './Mail.vue',
    './PlanDetail.vue',
    './Config.vue',
  ],
  { eager: true, query: '?raw', import: 'default' },
) as Record<string, string>

function page(name: string): string {
  const key = Object.keys(mods).find((k) => k.endsWith(`/${name}`))
  if (!key) throw new Error(`missing ${name}`)
  return mods[key]
}

/** 徽标必须紧跟在这个锚点片段之后（或就在它内部），避免"导入了但没摆出来"。 */
function expectBadgeAfter(src: string, anchor: string): void {
  const at = src.indexOf(anchor)
  expect(at, `anchor not found: ${anchor}`).toBeGreaterThan(-1)
  expect(src.slice(at, at + 400)).toContain('<ExperimentalBadge')
}

describe('experimental badges on the console entries', () => {
  it('workflow 模板 / 工作流：列表页标题与「从模板新建」页标题', () => {
    const list = page('Workflows.vue')
    expect(list).toContain(IMPORT)
    expectBadgeAfter(list, "t('workflows.title')")
    const create = page('NewWorkflow.vue')
    expect(create).toContain(IMPORT)
    expectBadgeAfter(create, "t('workflows.new.title')")
  })

  it('信箱：页面标题', () => {
    const mail = page('Mail.vue')
    expect(mail).toContain(IMPORT)
    expectBadgeAfter(mail, '<h1 class="mail-title">{{ t(\'mail.h.title\') }}')
  })

  it('leader 回合：plan 详情里的 leader 开关（徽标在 label 外，点它不改状态）', () => {
    const plan = page('PlanDetail.vue')
    expect(plan).toContain(IMPORT)
    expectBadgeAfter(plan, 'PLAN_CONTROL_HELP.leader.label')
    // 徽标不在 <label> 里：否则点徽标会顺手切换 leader。
    const badgeAt = plan.indexOf('<ExperimentalBadge />')
    const labelStart = plan.lastIndexOf('<label class="leader-switch mono"', badgeAt)
    const labelEnd = plan.lastIndexOf('</label>', badgeAt)
    expect(labelEnd).toBeGreaterThan(labelStart)
    expect(plan.slice(labelStart, labelEnd)).not.toContain('<ExperimentalBadge')
  })

  it('决策 hook：设置页 decision_hooks 字段标签', () => {
    const config = page('Config.vue')
    expect(config).toContain(IMPORT)
    expectBadgeAfter(config, 'decision_hooks（决策 hook')
  })
})

// 不标实验性：worktree 隔离与工作项页本身是稳定功能（docs/guide/stability.md 只把交付、收集箱与管家列为实验性）。
describe('stable entry points carry no experimental badge', () => {
  it('the work page title and the new-job worktree option', () => {
    const stable = import.meta.glob(['./Work.vue', './NewJob.vue'], { eager: true, query: '?raw', import: 'default' }) as Record<string, string>
    for (const src of Object.values(stable)) expect(src).not.toContain('ExperimentalBadge')
  })
})
