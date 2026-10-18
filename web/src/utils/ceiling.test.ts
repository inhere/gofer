// 调用方上限（ceiling）纯函数：语义必须与后端 internal/config/ceiling.go 逐条一致，
// 这份用例就是那条「两边同口径」的契约（同组用例两边各写一份）。
import { describe, expect, it } from 'vitest'
import {
  CEILING_LABEL,
  callerCeiling,
  ceilingAllowsAgent,
  ceilingAllowsExec,
  ceilingAllowsGlobalMemory,
  ceilingAllowsProject,
  ceilingAllowsRunner,
  ceilingAllowsWorker,
  ceilingDeniedText,
  ceilingLimitsProjects,
  ceilingReadOnly,
  ceilingSummary,
  globMatch,
  globToRegExp,
  hasCeiling,
  isMachineRunner,
  normalizeRunnerName,
} from './ceiling'
import type { AuthCeiling, AuthWhoami } from './permissions'

function ceil(over: Partial<AuthCeiling> = {}): AuthCeiling {
  return { allow_exec: false, global_memory: false, read_only: false, ...over }
}

describe('glob（path.Match 语义）', () => {
  it('`*` 不跨 /，`?` 单个字符', () => {
    expect(globToRegExp('app-*')!.test('app-web')).toBe(true)
    expect(globToRegExp('app-*')!.test('app-')).toBe(true)
    expect(globToRegExp('*')!.test('a/b')).toBe(false)
    expect(globToRegExp('**')!.test('a/b')).toBe(false)
    expect(globToRegExp('*/*')!.test('a/b')).toBe(true)
    expect(globToRegExp('a?c')!.test('abc')).toBe(true)
    expect(globToRegExp('a?c')!.test('ac')).toBe(false)
  })

  it('字符类与转义', () => {
    expect(globMatch(['[a-c]x'], 'bx')).toBe(true)
    expect(globMatch(['[^a-c]x'], 'dx')).toBe(true)
    expect(globMatch(['[^a-c]x'], 'bx')).toBe(false)
    expect(globMatch(['a\\*b'], 'a*b')).toBe(true)
    expect(globMatch(['a\\*b'], 'axb')).toBe(false)
  })

  it('非法模式不匹配任何值（后端加载期会拒绝这种配置）', () => {
    expect(globToRegExp('abc\\')).toBeNull()
    expect(globToRegExp('a[b')).toBeNull()
    expect(globMatch(['abc\\'], 'abc')).toBe(false)
  })
})

describe('project / worker / agent 维度', () => {
  it('没有上限 = 全放行', () => {
    expect(ceilingAllowsProject(null, 'p1')).toBe(true)
    expect(ceilingAllowsWorker(undefined, 'w1')).toBe(true)
    expect(ceilingAllowsAgent(null, 'claude')).toBe(true)
    expect(ceilingAllowsRunner(null, 'server')).toBe(true)
    expect(hasCeiling(null)).toBe(false)
  })

  it('空列表 = 该维度不限', () => {
    const c = ceil({ projects: [], workers: [], agents: [] })
    expect(ceilingAllowsProject(c, 'p1')).toBe(true)
    expect(ceilingAllowsWorker(c, 'w1')).toBe(true)
    expect(ceilingAllowsAgent(c, 'claude')).toBe(true)
    expect(ceilingLimitsProjects(c)).toBe(false)
  })

  it('非空列表按 glob 收窄，范围外一律拒', () => {
    const c = ceil({ projects: ['app-*', 'tools'], workers: ['w-?'], agents: ['claude*'] })
    expect(ceilingAllowsProject(c, 'app-web')).toBe(true)
    expect(ceilingAllowsProject(c, 'tools')).toBe(true)
    expect(ceilingAllowsProject(c, 'other')).toBe(false)
    expect(ceilingAllowsProject(c, '')).toBe(false)
    expect(ceilingAllowsWorker(c, 'w-1')).toBe(true)
    expect(ceilingAllowsWorker(c, 'w-10')).toBe(false)
    expect(ceilingAllowsAgent(c, 'claude-acp')).toBe(true)
    expect(ceilingAllowsAgent(c, 'codex')).toBe(false)
    expect(ceilingLimitsProjects(c)).toBe(true)
  })
})

describe('runner 维度（本机 runner 必须写明）', () => {
  it('大小写与两种拼写归一化', () => {
    expect(normalizeRunnerName('SERVER')).toBe('local')
    expect(normalizeRunnerName(' Local ')).toBe('local')
    expect(isMachineRunner('server')).toBe(true)
    expect(isMachineRunner('worker-1')).toBe(false)
  })

  it('空列表 = 只允许远程 worker（本机被排除）', () => {
    const c = ceil({ runners: [] })
    expect(ceilingAllowsRunner(c, 'w-docker')).toBe(true)
    expect(ceilingAllowsRunner(c, 'local')).toBe(false)
    expect(ceilingAllowsRunner(c, 'server')).toBe(false)
  })

  it('glob 永不匹配本机 runner；写明 local / server 才允许', () => {
    expect(ceilingAllowsRunner(ceil({ runners: ['*'] }), 'w1')).toBe(true)
    expect(ceilingAllowsRunner(ceil({ runners: ['*'] }), 'local')).toBe(false)
    expect(ceilingAllowsRunner(ceil({ runners: ['*'] }), 'server')).toBe(false)
    expect(ceilingAllowsRunner(ceil({ runners: ['local'] }), 'local')).toBe(true)
    expect(ceilingAllowsRunner(ceil({ runners: ['local'] }), 'server')).toBe(true)
    expect(ceilingAllowsRunner(ceil({ runners: ['server'] }), 'local')).toBe(true)
    expect(ceilingAllowsRunner(ceil({ runners: ['worker-*'] }), 'worker-2')).toBe(true)
    expect(ceilingAllowsRunner(ceil({ runners: ['worker-*'] }), 'peer-1')).toBe(false)
  })
})

describe('开关（whoami 给的是已生效的布尔值）', () => {
  it('没有上限时 exec / 全局记忆放行、只读为否', () => {
    expect(ceilingAllowsExec(null)).toBe(true)
    expect(ceilingAllowsGlobalMemory(undefined)).toBe(true)
    expect(ceilingReadOnly(null)).toBe(false)
  })

  it('有上限但没写 allow_exec / global_memory = 严格默认（不许）', () => {
    const c = ceil()
    expect(ceilingAllowsExec(c)).toBe(false)
    expect(ceilingAllowsGlobalMemory(c)).toBe(false)
    expect(ceilingReadOnly(c)).toBe(false)
  })

  it('显式允许才放行；read_only: true 就是只读', () => {
    expect(ceilingAllowsExec(ceil({ allow_exec: true }))).toBe(true)
    expect(ceilingAllowsGlobalMemory(ceil({ global_memory: true }))).toBe(true)
    expect(ceilingReadOnly(ceil({ read_only: true }))).toBe(true)
  })
})

describe('界面文案', () => {
  it('上限摘要逐维一行，runner 空列表说明只允许远程 worker', () => {
    const rows = ceilingSummary(ceil({ projects: ['app-*'], runners: ['worker-1'], read_only: true }))
    expect(rows).toContain('project：app-*')
    expect(rows).toContain('runner：worker-1')
    expect(rows).toContain('exec job：不允许')
    expect(rows).toContain('只读（不能写）：是')
    const empty = ceilingSummary(ceil({}))
    expect(empty).toContain('runner：仅远程 worker（不含本机 server）')
    expect(ceilingSummary(null)).toEqual([])
  })

  it('统一的原因文案', () => {
    expect(CEILING_LABEL).toBe('超出你的调用方上限')
    expect(ceilingDeniedText('runner', 'local')).toBe('超出你的调用方上限：runner local 不在你的范围内')
  })
})

describe('callerCeiling', () => {
  it('whoami 没有 ceiling 字段（旧服务端）时返回 null', () => {
    const who: AuthWhoami = {
      caller_id: 'alice',
      kind: 'user',
      root: false,
      permissions: [],
      grants_version: 1,
    }
    expect(callerCeiling(who)).toBeNull()
    expect(callerCeiling(null)).toBeNull()
    const withCeil = { ...who, ceiling: ceil({ projects: ['p'] }) }
    expect(callerCeiling(withCeil)?.projects).toEqual(['p'])
  })
})
