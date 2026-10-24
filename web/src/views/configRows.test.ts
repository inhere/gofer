import { describe, expect, it } from 'vitest'
import { cfgItems, cfgRows, formatCfgValue, type CfgLabels } from './configRows'

// 配置总览的行构造（gofer-4uk5）：GET /v1/config 带回了每个顶层段，控制台按分组展平成
// 「键 = 值」。这里测的是纯函数的格式化规则——尤其是敏感字段的「已配置 / 未配置」，
// 它与普通布尔的「是 / 否」必须是两种显示。
const zh: CfgLabels = { yes: '是', no: '否', set: '已配置', unset: '未配置', def: '默认', sep: ', ' }
const en: CfgLabels = { yes: 'Yes', no: 'No', set: 'Configured', unset: 'Not configured', def: 'default', sep: ', ' }

describe('配置总览的行构造', () => {
  it('*_set 是「已配置 / 未配置」，不是「是 / 否」', () => {
    expect(formatCfgValue('token_set', true, zh)).toBe('已配置')
    expect(formatCfgValue('proxy_url_set', false, zh)).toBe('未配置')
    expect(formatCfgValue('secret_env_set', true, en)).toBe('Configured')
    // 普通布尔仍然走「是 / 否」。
    expect(formatCfgValue('enabled', true, zh)).toBe('是')
    expect(formatCfgValue('enabled', false, en)).toBe('No')
  })

  it('null 显示默认（与 0 / 显式 false 区分），空串与空列表显示占位', () => {
    expect(formatCfgValue('auto_relay_idle_sec', null, zh)).toBe('默认')
    expect(formatCfgValue('auto_relay_idle_sec', 0, zh)).toBe('0')
    expect(formatCfgValue('digest_time', '', zh)).toBe('-')
    expect(formatCfgValue('inject_commands', [], zh)).toBe('-')
    expect(formatCfgValue('inject_commands', ['/clear', '/compact'], zh)).toBe('/clear, /compact')
    expect(formatCfgValue('stall_after', '45m', zh)).toBe('45m')
  })

  it('嵌套段展平成键路径，空对象不产生行', () => {
    const rows = cfgRows({ tls: { addr: ':8443', cert_file: '/c.pem' }, log: { max_size_mb: 50 } }, zh)
    expect(rows.map((r) => [r.label, r.value])).toEqual([
      ['tls.addr', ':8443'],
      ['tls.cert_file', '/c.pem'],
      ['log.max_size_mb', '50'],
    ])
    expect(cfgRows({}, zh)).toEqual([])
    expect(cfgRows(undefined, zh)).toEqual([])
  })

  it('敏感字段只给状态行：展平后的行里没有可以回车间的值', () => {
    // 服务端的 secrets 视图只有 key / description / as / set（env 名字不回声）。
    const [item] = cfgItems([{ key: 'deploy-token', description: 'deploy', as: 'DEPLOY_TOKEN', set: true }], 'key', zh) ?? []
    expect(item.label).toBe('deploy-token')
    const labels = item.rows.map((r) => r.label)
    expect(labels).toEqual(['description', 'as', 'set'])
    const setRow = item.rows.find((r) => r.label === 'set')
    expect(setRow?.value).toBe('已配置')
    expect(setRow?.set).toBe(true)
  })

  it('列表型段按标题键取条目名，其余字段成行', () => {
    const items = cfgItems(
      [
        { name: 'gh', desc: 'GitHub CLI', bin: 'gh' },
        { name: 'rg', desc: '', bin: 'rg' },
      ],
      'name',
      zh,
    )
    expect(items?.map((i) => i.label)).toEqual(['gh', 'rg'])
    expect(items?.[0].rows.map((r) => [r.label, r.value])).toEqual([
      ['desc', 'GitHub CLI'],
      ['bin', 'gh'],
    ])
    // 空描述显示占位，而不是空字串。
    expect(items?.[1].rows[0]).toEqual({ label: 'desc', value: '-', set: undefined })
    expect(cfgItems(undefined, 'name', zh)).toEqual([])
  })
})
