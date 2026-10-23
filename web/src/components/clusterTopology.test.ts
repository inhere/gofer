// gofer-hez1：运行节点拓扑曾同时出现中心「server」与辐射节点「server (本机)」（in-process
// local runner），看起来像两个 server。local 不再画节点，信息并入中心节点与它的面板。
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import ClusterTopology from './ClusterTopology.vue'
import type { Runner } from '../api/types'

const source = Object.values(
  import.meta.glob('./ClusterTopology.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

function runner(name: string, type: Runner['type'], status: Runner['status']): Runner {
  const worker = type === 'worker' ? { last_heartbeat: 1_000_000, heartbeat_age_ms: 0, in_flight: 1 } : undefined
  return { name, type, status, worker }
}

const render = (runners: Runner[], projects: string[] = []): Promise<string> =>
  renderToString(createSSRApp({ render: () => h(ClusterTopology, { runners, projects, nowMs: 1_000_500 }) }))

describe('ClusterTopology server node', () => {
  it('draws one server hub and no separate local (in-process) node', async () => {
    const html = await render([
      runner('local', 'local', 'up'),
      runner('w-docker', 'worker', 'connected'),
      runner('w-win11', 'worker', 'connected'),
    ])
    // 中心只有一个 hub 节点
    expect(html.match(/class="node node--hub"/g)).toHaveLength(1)
    // 辐射节点 = 两个 worker；local 不再单独成节点
    expect(html.match(/class="node"/g)).toHaveLength(2)
    expect(html).not.toContain('server (本机)')
    expect(html).toContain('data-test="topo-hub"')
  })

  it('keeps the local runner info inside the hub panel', () => {
    // hub 面板里补上本机 runner 的说明与 server 项目，信息不丢
    expect(source).toContain("runners.value.find((r) => r.type === 'local')")
    expect(source).toContain('data-test="topo-hub-local"')
    expect(source).toContain("t('runners.topo.localRunner')")
    expect(source).toContain("t('runners.topo.runInProcess')")
    expect(source).toContain("t('runners.topo.serverProjects'")
    // 排序只收集 worker 与 peer
    expect(source).toContain("runners.value.filter((r) => r.type === 'worker')")
    expect(source).toContain("runners.value.filter((r) => r.type === 'peer-http')")
  })

  it('still draws peers as radiating nodes', async () => {
    const html = await render([runner('peer-a', 'peer-http', 'up')])
    expect(html.match(/class="node"/g)).toHaveLength(1)
    expect(html).toContain('peer-a')
  })
})
