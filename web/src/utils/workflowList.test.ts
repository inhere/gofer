import { describe, expect, it } from 'vitest'
import {
  groupTemplates,
  isEditableLayer,
  layerOf,
  queryString,
  runCount,
  runLink,
  runsLink,
  templateLink,
  varNames,
} from './workflowList'
import { templateEditGate } from './workflowResult'
import type { WorkflowTemplateInfo } from '../api/types'

function tpl(name: string, source?: string, extra: Partial<WorkflowTemplateInfo> = {}): WorkflowTemplateInfo {
  return { name, source, spec: { steps: [] }, ...extra }
}

describe('groupTemplates', () => {
  it('按 项目 → 全局 → 内置 分组，空组不返回，组内保持原顺序', () => {
    const groups = groupTemplates([tpl('b1', 'builtin'), tpl('g1', 'global'), tpl('p1', 'project'), tpl('b2', 'builtin')])
    expect(groups.map((g) => g.layer)).toEqual(['project', 'global', 'builtin'])
    expect(groups[2].items.map((x) => x.name)).toEqual(['b1', 'b2'])
    expect(groupTemplates([tpl('g1', 'global')]).map((g) => g.layer)).toEqual(['global'])
    expect(groupTemplates([])).toEqual([])
  })

  it('未知或缺省来源按内置处理，模板不会从列表里消失', () => {
    expect(layerOf(tpl('x'))).toBe('builtin')
    expect(layerOf(tpl('x', 'future-layer'))).toBe('builtin')
    expect(groupTemplates([tpl('x', 'future-layer')])[0].items).toHaveLength(1)
  })
})

describe('编辑权限', () => {
  it('只有全局 / 项目文件可编辑，内置不可', () => {
    expect(isEditableLayer('project')).toBe(true)
    expect(isEditableLayer('global')).toBe(true)
    expect(isEditableLayer('builtin')).toBe(false)
    expect(isEditableLayer(undefined)).toBe(false)
  })

  it('没有 config.write 时编辑 / 删除不可用；whoami 未取到时按有权限（由服务端 403 说话）', () => {
    expect(templateEditGate(false, true).editable).toBe(false)
    expect(templateEditGate(true, true).editable).toBe(true)
    expect(templateEditGate(false, false).editable).toBe(true)
  })
})

describe('列表字段', () => {
  it('runCount 缺省为 0，负数也按 0', () => {
    expect(runCount({})).toBe(0)
    expect(runCount({ runs: 7 })).toBe(7)
    expect(runCount({ runs: -1 })).toBe(0)
  })

  it('varNames 按名字排序并标出必填', () => {
    const t = tpl('x', 'global', { spec: { steps: [], vars: { z: {}, a: { required: true } } } })
    expect(varNames(t)).toEqual([
      { name: 'a', required: true },
      { name: 'z', required: false },
    ])
    expect(varNames(tpl('y'))).toEqual([])
  })
})

describe('深链', () => {
  it('运行带 template，项目模板再带 project', () => {
    expect(runLink('review')).toBe('/workflows/new?template=review')
    expect(runLink('review', 'demo')).toBe('/workflows/new?template=review&project=demo')
  })
  it('名字做 URL 编码', () => {
    expect(runsLink('a b')).toBe('/workflows/runs?template=a%20b')
    expect(templateLink('a/b', 'p')).toBe('/workflows/templates/a%2Fb?project=p')
    expect(templateLink('plain')).toBe('/workflows/templates/plain')
  })
  it('queryString 取首个字符串，其余给空串', () => {
    expect(queryString('x')).toBe('x')
    expect(queryString(['a', 'b'])).toBe('a')
    expect(queryString(null)).toBe('')
    expect(queryString(undefined)).toBe('')
  })
})
