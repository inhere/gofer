// 只读参考目录 read_dirs（gofer-x4h1）的前端接线：新建 job 表单、待办编辑 / 新建、待办抽屉、
// job 详情。这些页面依赖路由 / 网络，现有测试对它们用「源码断言 + SSR 渲染」，这里同样做法。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import TodoDrawer from '../components/TodoDrawer.vue'
import { setLocale } from '../store/locale'
import type { Todo } from '../api/types'

// 测试环境没有 DOM，MarkdownBlock 的 DOMPurify 需要 window：把 sanitize 当透传。
vi.mock('dompurify', () => ({ default: { sanitize: (html: string) => html } }))
vi.mock('../api/client', () => ({
  listEvents: async () => ({ events: [] }),
  listComments: async () => [],
  postComment: async () => ({ id: 'c1' }),
}))

const raw = (mods: Record<string, unknown>): string => Object.values(mods)[0] as string
const newJob = raw(import.meta.glob('./NewJob.vue', { eager: true, query: '?raw', import: 'default' }))
const jobDetail = raw(import.meta.glob('./JobDetail.vue', { eager: true, query: '?raw', import: 'default' }))
const planDetail = raw(import.meta.glob('./PlanDetail.vue', { eager: true, query: '?raw', import: 'default' }))

describe('new job read_dirs field', () => {
  it('puts non-empty trimmed lines into the submit payload as read_dirs', () => {
    expect(newJob).toContain(".split('\\n').map((l) => l.trim()).filter(Boolean)")
    expect(newJob).toContain('req.read_dirs = readDirList')
    expect(newJob).toContain('v-model="readDirs"')
  })

  it('disables the textarea, explains why, and sends nothing when the agent has no add_dir_args', () => {
    expect(newJob).toContain(':disabled="!readDirsSupported"')
    expect(newJob).toContain('data-test="read-dirs-unsupported"')
    expect(newJob).toContain('const readDirList = readDirsSupported.value ? parseReadDirs(readDirs.value) : []')
    expect(newJob).toContain('(a.add_dir_args?.length ?? 0) > 0')
  })
})

describe('todo read_dirs', () => {
  const todo: Todo = {
    todo_id: 't-1',
    plan_id: 'p-1',
    title: '读参考目录',
    done: false,
    status: 'ready',
    read_dirs: ['docs', '/srv/shared'],
    created_at: 1_700_000_000,
    updated_at: 1_700_000_100,
  }

  async function render(t: Todo): Promise<string> {
    const ctx: { teleports?: Record<string, string> } = {}
    const app = createSSRApp({ render: () => h(TodoDrawer, { todo: t, todos: [t], jobs: [], agents: [] } as never) })
    await renderToString(app, ctx)
    return ctx.teleports?.body ?? ''
  }

  it('drawer lists the read dirs, and omits the row when empty', async () => {
    setLocale('zh-CN')
    const html = await render(todo)
    expect(html).toContain('只读参考目录')
    expect(html).toContain('docs, /srv/shared')
    expect(await render({ ...todo, read_dirs: undefined })).not.toContain('只读参考目录')
  })

  it('edit panel saves read_dirs (explicit [] clears) and the add form passes it to addTodo', () => {
    expect(planDetail).toContain('v-model="editForm.readDirs"')
    expect(planDetail).toContain('patch.read_dirs = readDirs')
    expect(planDetail).toContain('(t.read_dirs?.length ?? 0) > 0')
    expect(planDetail).toContain('v-model="newTodoReadDirs"')
    expect(planDetail).toContain("newTodoReadDirs.value.split('\\n')")
  })
})

describe('job detail read_dirs', () => {
  it('renders a list only when read_dirs is non-empty', () => {
    expect(jobDetail).toContain('v-if="job.read_dirs?.length"')
    expect(jobDetail).toContain('v-for="d in job.read_dirs"')
    expect(jobDetail).toContain('<span class="meta-k mono">read_dirs</span>')
  })
})
