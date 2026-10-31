import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { JobPreview } from '../api/preview'
import { dicts } from '../i18n'
import { errors } from '../i18n/pages/errors'
import JobPreviews from './JobPreviews.vue'

const raw = (g: Record<string, unknown>): string => Object.values(g)[0] as string
const componentSource = raw(import.meta.glob('./JobPreviews.vue', { eager: true, query: '?raw', import: 'default' }))
const controllerSource = raw(import.meta.glob('../utils/jobPreviews.ts', { eager: true, query: '?raw', import: 'default' }))
const apiSource = raw(import.meta.glob('../api/preview.ts', { eager: true, query: '?raw', import: 'default' }))
const jobDetailSource = raw(import.meta.glob('../views/JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))

// 去掉注释再做「不含某某」的断言（注释里会解释为什么不碰 storage / Bearer）。
const code = (src: string): string => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '').replace(/^\s*\/\/.*$/gm, '')

const TOKEN = 'tok-SECRET-preview'

const rows: JobPreview[] = [
  { id: 'pv-active', job_id: 'j1', port: 18100, target_port: 3000, state: 'active', expires_at: 1900000000 },
  { id: 'pv-closed', job_id: 'j1', port: 18101, state: 'closed', expires_at: 1900000000 },
]

function render(props: Record<string, unknown>): Promise<string> {
  return renderToString(createSSRApp({ render: () => h(JobPreviews as never, props) }))
}

beforeEach(() => {
  vi.stubGlobal('localStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {} })
  vi.stubGlobal('sessionStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {} })
})
afterEach(() => vi.unstubAllGlobals())

describe('JobPreviews rendering', () => {
  it('shows the form, the list rows with state and host, and never a token', async () => {
    const html = await render({ jobId: 'j1', status: 'running', initialItems: rows })
    expect(html).toContain('data-test="preview-form"')
    expect(html).toContain('data-test="preview-port"')
    expect(html).toContain('data-test="preview-ttl"')
    expect(html).toContain('data-test="preview-open-now"')
    expect(html).toContain('data-test="preview-create"')
    expect(html).toContain('data-preview-id="pv-active"')
    expect(html).toContain('data-preview-id="pv-closed"')
    expect(html).toContain(':18100')
    expect(html).toContain('data-status="active"')
    // 运行中不出现「不在运行」提示
    expect(html).not.toContain('data-test="preview-not-running"')
    expect(html).not.toContain(TOKEN)
    expect(html).not.toContain('token=')
  })

  it('offers renew and revoke only on an active lease, and no open link without a fresh url', async () => {
    const html = await render({ jobId: 'j1', status: 'running', initialItems: rows })
    expect(html.match(/data-test="preview-renew"/g)?.length).toBe(1)
    expect(html.match(/data-test="preview-revoke"/g)?.length).toBe(1)
    // 一次性 URL 只在创建当时存在；刷新后的列表没有打开按钮
    expect(html).not.toContain('data-test="preview-open"')
    // 撤销是二次确认：初始不显示确认按钮
    expect(html).not.toContain('data-test="preview-revoke-confirm"')
  })

  it('disables creating and says why when the job is not running', async () => {
    const html = await render({ jobId: 'j1', status: 'done', initialItems: [] })
    expect(html).toContain('data-test="preview-not-running"')
    expect(html).toContain('data-test="preview-empty"')
    expect(html).toMatch(/data-test="preview-create"[^>]*disabled|disabled[^>]*data-test="preview-create"/)
    expect(html).toMatch(/data-test="preview-port"[^>]*disabled|disabled[^>]*data-test="preview-port"/)
  })

  it('renders in both locales', async () => {
    const { locale } = await import('../store/locale')
    locale.value = 'en-US'
    expect(await render({ jobId: 'j1', status: 'done' })).toContain('The job is not running, so no new preview can be created')
    locale.value = 'zh-CN'
    expect(await render({ jobId: 'j1', status: 'done' })).toContain('job 当前不在运行，不能创建新预览')
  })
})

describe('JobPreviews wiring', () => {
  it('is mounted in the job detail overview with the job id and status', () => {
    expect(jobDetailSource).toContain("import JobPreviews from '../components/JobPreviews.vue'")
    expect(jobDetailSource).toContain(`<JobPreviews v-if="active === 'overview' && job" :job-id="job.id" :status="status" />`)
  })

  it('reuses StatusPill / Toast / Switch and reloads when the job status changes', () => {
    for (const c of ['StatusPill', 'Toast', 'Switch']) expect(componentSource).toContain(`import ${c} from '../ui/${c}.vue'`)
    expect(componentSource).toContain('() => props.status')
  })

  it('confirms before revoking and opens in a new tab without referrer / opener', () => {
    expect(componentSource).toContain('ctl.askRevoke(p.id)')
    expect(componentSource).toContain('ctl.confirmRevoke()')
    expect(controllerSource).toContain("window.open(url, '_blank', 'noopener,noreferrer')")
  })

  it('keeps the one-time url out of storage and out of the api client', () => {
    for (const src of [componentSource, controllerSource, apiSource]) {
      expect(code(src)).not.toMatch(/localStorage|sessionStorage|document\.cookie/)
    }
    // 打开 URL 不经 request() / fetch / Authorization
    expect(code(controllerSource)).not.toMatch(/fetch\(|Authorization|Bearer/)
    expect(code(componentSource)).not.toMatch(/fetch\(|Authorization|Bearer/)
    expect(controllerSource).toContain('openUrls')
  })

  it('has no hard-coded user-facing text: every t() key resolves in both locales', () => {
    const keys = new Set<string>()
    for (const m of componentSource.matchAll(/\bt\('([^']+)'/g)) keys.add(m[1])
    expect(keys.size).toBeGreaterThan(15)
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const k of keys) expect(dicts[loc][k], `${loc} ${k}`).toBeTruthy()
    }
  })
})

describe('preview i18n keys', () => {
  it('has the same jobs.detail.preview.* keys in zh and en, with the same placeholders, and no CJK in English', () => {
    const pick = (d: Record<string, string>) => Object.keys(d).filter((k) => k.startsWith('jobs.detail.preview.')).sort()
    const zh = pick(dicts['zh-CN'])
    expect(zh.length).toBeGreaterThan(25)
    expect(pick(dicts['en-US'])).toEqual(zh)
    const names = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort()
    for (const k of zh) {
      expect(names(dicts['en-US'][k]), k).toEqual(names(dicts['zh-CN'][k]))
      expect(dicts['en-US'][k], k).not.toMatch(/\p{Script=Han}/u)
    }
    // 动态拼的 key：状态与提示条
    for (const s of ['active', 'expired', 'closed', 'job_ended', 'worker_lost']) expect(dicts['en-US'][`jobs.detail.preview.state.${s}`], s).toBeTruthy()
    for (const n of ['created', 'renewed', 'revoked']) expect(dicts['zh-CN'][`jobs.detail.preview.notice.${n}`], n).toBeTruthy()
  })

  it('has the err.<code> sentences for every preview refusal in both locales', () => {
    const codes = [
      'preview_disabled',
      'preview_job_not_running',
      'preview_runner_unsupported',
      'preview_port_invalid',
      'preview_pool_exhausted',
      'preview_not_active',
    ]
    for (const c of codes) {
      expect(errors.zh[`err.${c}`], `zh ${c}`).toBeTruthy()
      expect(errors.en[`err.${c}`], `en ${c}`).toBeTruthy()
      expect(errors.en[`err.${c}`]).not.toMatch(/\p{Script=Han}/u)
    }
    expect(errors.en['err.preview_runner_unsupported']).toContain('{runner}')
    expect(errors.zh['err.preview_runner_unsupported']).toContain('{runner}')
  })
})
