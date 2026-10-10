import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { ApiError } from '../api/client'
import type { Job } from '../api/types'
import { approvalErrorText, commandLine, countdownText, createApprovalActions, shellQuote } from '../utils/approval'
import ApprovalPanel from './ApprovalPanel.vue'
import StatusBadge from './StatusBadge.vue'

const raw = (glob: Record<string, unknown>) => Object.values(glob)[0] as string
const panelSrc = raw(import.meta.glob('./ApprovalPanel.vue', { eager: true, query: '?raw', import: 'default' }))
const detailSrc = raw(import.meta.glob('../views/JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))

const NOW = 1_760_000_000

// 一条手机上必然折行的长命令：375px 宽度下也得完整渲染，不截断、不靠横向滚动。
const LONG_ARGV = [
  'git',
  'push',
  '--force-with-lease=refs/heads/feature/very-long-branch-name-for-mobile-wrapping:0123456789abcdef0123456789abcdef01234567',
  'origin',
  'HEAD:refs/heads/feature/very-long-branch-name-for-mobile-wrapping',
  '-o',
  "ci.variable=NOTE=it's done",
]

function heldJob(over: Partial<Job> = {}): Job {
  return {
    id: 'j-held-1',
    project_key: 'demo',
    agent: 'exec',
    runner: 'local',
    status: 'awaiting_approval',
    exit_code: 0,
    cwd: '/work/demo',
    result_dir: '/tmp/r',
    started_at: NOW - 600,
    caller_id: 'alice',
    channel: 'cli',
    require_review: true,
    requested_timeout_sec: 300,
    hold: {
      reason: '推送发布分支（agent 权限拦下了 git push）',
      timeout_sec: 86400,
      expires_at: NOW + 2 * 3600 + 5 * 60,
      origin: 'agent-session:s-42',
      command: LONG_ARGV,
    },
    ...over,
  }
}

function decode(html: string): string {
  return html.replace(/&#39;/g, "'").replace(/&quot;/g, '"').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&')
}

async function render(component: unknown, props: Record<string, unknown>): Promise<string> {
  return decode(await renderToString(createSSRApp({ render: () => h(component as never, props) })))
}

describe('ApprovalPanel', () => {
  it('renders the whole command at phone width: wrapped, never truncated or scrolled sideways', async () => {
    const html = await render(ApprovalPanel, { job: heldJob(), nowSec: NOW })
    // 全文（含 shell 引号拼接）原样在 <pre> 里
    expect(html).toContain(commandLine(LONG_ARGV))
    expect(html).toContain(`'ci.variable=NOTE=it'\\''s done'`)
    expect(html).toContain('推送发布分支（agent 权限拦下了 git push）')
    // runner 显示用 runnerLabel：local → server
    expect(html).toMatch(/runner<\/dt><dd[^>]*>server/)
    expect(html).toContain('agent 会话 s-42')
    expect(html).toContain('caller alice')
    expect(html).toContain('还剩 2 小时 5 分')
    expect(html).toContain('执行后要人验收')
    expect(html).toContain('job 超时 5m')
    // 375px 宽度靠 CSS 折行：pre-wrap + break-all，且没有横向滚动
    expect(panelSrc).toMatch(/\.ap-cmd \{[^}]*white-space: pre-wrap;[^}]*word-break: break-all;[^}]*overflow-x: hidden;/)
    expect(panelSrc).not.toMatch(/\.ap-cmd \{[^}]*overflow-x: auto/)
    // ≤640px：按钮条贴底，触控高度 ≥44px
    expect(panelSrc).toMatch(/@media \(max-width: 640px\) \{[\s\S]*?\.ap-actions \{[^}]*position: sticky;[^}]*bottom: 0;/)
    expect(panelSrc).toMatch(/@media \(max-width: 640px\) \{[\s\S]*?\.ap-btn \{[^}]*min-height: 44px;/)
    // 拒绝理由框默认收起
    expect(html).not.toContain('data-test="ap-reject-form"')
  })

  it('shows an agent job prompt preview expanded by default', async () => {
    const job = heldJob({
      agent: 'claude',
      hold: { reason: '', timeout_sec: 3600, expires_at: NOW - 1, origin: 'web', prompt_preview: '把 v1.2.0 打 tag 并推送\n第二行' },
    })
    const html = await render(ApprovalPanel, { job, nowSec: NOW })
    expect(html).toMatch(/<details[^>]*open/)
    expect(html).toContain('把 v1.2.0 打 tag 并推送\n第二行')
    expect(html).toContain('（提交者没写理由）')
    expect(html).toContain('已过期')
  })

  it('approves with a single click (no confirm) and swallows repeated clicks while in flight', async () => {
    let release: (j: Job) => void = () => {}
    const approve = vi.fn((_id: string, _note?: string) => new Promise<Job>((r) => (release = r)))
    const reject = vi.fn()
    const done = vi.fn()
    const ctl = createApprovalActions(() => 'j1', { approve, reject }, done)
    const first = ctl.approve()
    void ctl.approve()
    expect(approve).toHaveBeenCalledTimes(1)
    expect(approve).toHaveBeenCalledWith('j1')
    expect(ctl.busy.value).toBe('approve')
    release(heldJob({ status: 'queued' }))
    await first
    expect(done).toHaveBeenCalledTimes(1)
    expect(ctl.busy.value).toBe('')
    expect(reject).not.toHaveBeenCalled()
    // 「批准」按钮直接绑定 approve，没有二次确认
    expect(panelSrc).toMatch(/data-test="ap-approve"[\s\S]*?@click="actions\.approve"/)
    expect(panelSrc).not.toMatch(/confirm\(/)
  })

  it('lets a reject go out with an empty reason', async () => {
    const approve = vi.fn()
    const reject = vi.fn(async () => heldJob({ status: 'cancelled' }))
    const done = vi.fn()
    const ctl = createApprovalActions(() => 'j2', { approve, reject }, done)
    ctl.openReject()
    expect(ctl.rejectOpen.value).toBe(true)
    await ctl.reject()
    expect(reject).toHaveBeenCalledWith('j2', '', false)
    expect(done).toHaveBeenCalledTimes(1)
    expect(ctl.rejectOpen.value).toBe(false)

    ctl.openReject()
    ctl.rejectNote.value = '  先等 CI  '
    await ctl.reject()
    expect(reject).toHaveBeenLastCalledWith('j2', '先等 CI', false)
  })

  it('maps error codes to Chinese hints and keeps the panel usable', async () => {
    expect(approvalErrorText(new ApiError(409, 'approve failed', 'job is not awaiting approval'), '批准')).toContain('已被处理，或请求在等待期间变了')
    expect(approvalErrorText(new ApiError(503, 'approve failed'), '批准')).toContain('服务升级中，请稍后再批')
    expect(approvalErrorText(new ApiError(403, 'approve not permitted'), '拒绝')).toContain('无权限')
    expect(approvalErrorText(new Error('network down'), '批准')).toBe('批准失败：network down')

    const ctl = createApprovalActions(
      () => 'j3',
      { approve: async () => Promise.reject(new ApiError(503, 'approve failed')), reject: vi.fn() },
      vi.fn(),
    )
    await ctl.approve()
    expect(ctl.error.value).toContain('服务升级中')
    expect(ctl.busy.value).toBe('')
  })

  it('quotes argv like a shell and counts down to the expiry', () => {
    expect(shellQuote('')).toBe("''")
    expect(shellQuote('a b')).toBe("'a b'")
    expect(shellQuote('HEAD:main')).toBe('HEAD:main')
    expect(commandLine(['sh', '-c', 'date > probe'])).toBe("sh -c 'date > probe'")
    expect(countdownText(NOW + 90, NOW)).toBe('还剩 1 分钟')
    expect(countdownText(NOW + 2 * 86400 + 3600, NOW)).toBe('还剩 2 天 1 小时')
    expect(countdownText(NOW, NOW)).toBe('已过期')
  })
})

describe('job detail hosts the approval panel', () => {
  it('shows it only while awaiting approval, between the header and the meta block', () => {
    expect(detailSrc).toMatch(/showApprovalPanel = computed<boolean>\(\(\) => job\.value\?\.status === 'awaiting_approval'\)/)
    const header = detailSrc.indexOf('</header>')
    const panel = detailSrc.indexOf('<ApprovalPanel')
    const meta = detailSrc.indexOf('<div v-if="job" class="meta">')
    expect(header).toBeGreaterThan(0)
    expect(panel).toBeGreaterThan(header)
    expect(meta).toBeGreaterThan(panel)
    // 已决定的 hold 在 meta 区回显
    expect(detailSrc).toContain('data-test="hold-decision"')
  })
})

describe('status badge for held jobs', () => {
  it('reads 「⏸ 待批准」 and pulses like the other wait-for-a-person states', async () => {
    const html = await render(StatusBadge, { status: 'awaiting_approval' })
    expect(html).toContain('⏸ 待批准')
    expect(html).toContain('badge--attn')
  })
})
