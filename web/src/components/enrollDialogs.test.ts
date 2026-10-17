import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { WorkerEnrollment } from '../api/types'
import EnrollAcceptDialog from './EnrollAcceptDialog.vue'
import EnrollRejectDialog from './EnrollRejectDialog.vue'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const acceptSource = raw(import.meta.glob('./EnrollAcceptDialog.vue', { eager: true, query: '?raw', import: 'default' }))
const rejectSource = raw(import.meta.glob('./EnrollRejectDialog.vue', { eager: true, query: '?raw', import: 'default' }))

const NOW = 1_760_000_000

const item: WorkerEnrollment = {
  id: 'we_1',
  state: 'pending',
  requested_id: 'gpu1',
  labels: ['linux'],
  projects: [],
  host: { hostname: 'box-1', os: 'linux', arch: 'amd64', gofer_version: '0.168.0', protocol_version: '15', agents: ['claude'], roots: ['/srv/work'] },
  source: '10.0.0.9',
  created_at: NOW - 10,
  decide_until: NOW + 80,
}

function render(component: unknown, props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(component as never, props) }))
}

describe('EnrollAcceptDialog', () => {
  it('asks for the pairing code in two 4-character fields and never shows a code', async () => {
    const html = await render(EnrollAcceptDialog, { item, projects: ['p1', 'p2'], nowSec: NOW, existingWorkerIds: [] })
    expect(html).toContain('data-test="enroll-accept-dialog"')
    expect(html.match(/maxlength="4"/g)?.length).toBe(2)
    expect(html).toContain('data-test="enroll-code-first"')
    expect(html).toContain('data-test="enroll-code-second"')
    // 码从 worker 终端上来，web 只是一张空输入
    expect(html).toContain('配对码在 worker 终端上')
    expect(html).not.toContain('pairing_code')
  })

  it('prefills the proposed worker id, the labels and the projects to choose from', async () => {
    const html = await render(EnrollAcceptDialog, { item, projects: ['p1', 'p2'], nowSec: NOW, existingWorkerIds: [] })
    expect(html).toContain('data-test="enroll-worker-id"')
    expect(html).toContain('value="gpu1"')
    expect(html).toContain('value="linux"')
    expect(html).toContain('data-test="enroll-project-p1"')
    expect(html).toContain('data-test="enroll-project-p2"')
    // 主机信息与倒计时作为取舍依据
    expect(html).toContain('10.0.0.9')
    expect(html).toContain('linux/amd64 · gofer v0.168.0 · 协议 v15')
    expect(html).toContain('还剩 1 分 20 秒')
    expect(html).toContain('data-test="enroll-accept-submit"')
  })

  it('sends the normalized code and points at the reserved names via pure helpers', () => {
    // 两段各自清洗（大写、吃掉分隔符）后拼成提交值；保留名当场提示
    expect(acceptSource).toContain('pairingFromSegments(codeFirst.value, codeSecond.value)')
    expect(acceptSource).toContain('buildAcceptRequest(form, props.existingWorkerIds ?? [])')
    expect(acceptSource).toContain('workerIdError')
    expect(acceptSource).toContain('acceptErrorText(e)')
  })
})

describe('EnrollRejectDialog', () => {
  it('offers an optional reason and the 10-minute source block', async () => {
    const html = await render(EnrollRejectDialog, { item })
    expect(html).toContain('data-test="enroll-reject-dialog"')
    expect(html).toContain('data-test="enroll-reject-reason"')
    expect(html).toContain('data-test="enroll-reject-block"')
    expect(html).toContain('屏蔽该来源 10 分钟')
    expect(html).toContain('10.0.0.9')
  })

  it('posts reason + block_source_min and maps 409 / 403 to Chinese', () => {
    expect(rejectSource).toContain('block_source_min: block.value ? BLOCK_SOURCE_MIN : 0')
    expect(rejectSource).toContain('const BLOCK_SOURCE_MIN = 10')
    expect(rejectSource).toContain('rejectErrorText(e)')
  })
})
