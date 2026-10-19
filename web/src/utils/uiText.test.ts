// 界面文字（导航 / 页面标题 / 面包屑 / 常用列名）的纯函数断言（用户 2026-10-19 的中文口径）。
// 接线（各页面真的引用了它们）在 views/uiTextWiring.test.ts 里用 ?raw 源码断言。
import { describe, expect, it } from 'vitest'
import { BACK, COL, NAV, PAGE } from './uiText'

describe('uiText 中文口径', () => {
  it('导航按对照表用中文，Issues / Agents / Runners 保留英文', () => {
    expect(NAV.work).toBe('工作项')
    expect(NAV.plans).toBe('计划')
    expect(NAV.jobs).toBe('任务')
    expect(NAV.sessions).toBe('会话')
    expect(NAV.dashboard).toBe('统计')
    expect(NAV.projects).toBe('项目')
    expect(NAV.workflows).toBe('工作流')
    expect(NAV.schedules).toBe('定时')
    // 专有名词保留英文。
    expect(NAV.issues).toBe('Issues')
    expect(NAV.agents).toBe('Agents')
    expect(NAV.runners).toBe('Runners')
  })

  it('页面标题：任务列表页是「任务看板」，其余与导航同名', () => {
    expect(PAGE.board).toBe('任务看板')
    expect(PAGE.plans).toBe(NAV.plans)
    expect(PAGE.sessions).toBe(NAV.sessions)
    expect(PAGE.workflows).toBe(NAV.workflows)
    expect(PAGE.projects).toBe(NAV.projects)
    expect(PAGE.schedules).toBe(NAV.schedules)
    expect(PAGE.dashboard).toBe(NAV.dashboard)
    expect(PAGE.review).toBe('待验收')
    expect(PAGE.settings).toBe('设置')
    expect(PAGE.newJob).toBe('新建任务')
  })

  it('面包屑指向中文页面名', () => {
    expect(BACK.board).toBe('← 任务看板')
    expect(BACK.schedules).toBe('← 定时')
    expect(BACK.workflows).toBe('← 工作流')
    expect(BACK.plans).toBe('← 计划')
  })

  it('常用列名是中文，agent / runner 这类专有名词保留英文写法', () => {
    expect(COL.status).toBe('状态')
    expect(COL.project).toBe('项目')
    expect(COL.tags).toBe('标签')
    expect(COL.agent).toBe('Agent')
    expect(COL.runner).toBe('Runner')
  })

  it('没有把英文旧词留在导航文案里', () => {
    const all = [...Object.values(NAV), ...Object.values(PAGE), ...Object.values(COL)]
    for (const old of ['Works', 'Plans', 'Jobs', 'Sessions', 'Dashboard', 'Projects', 'Workflows', 'Schedules']) {
      expect(all).not.toContain(old)
    }
  })
})
