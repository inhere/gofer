// 待办详情抽屉（components/TodoDrawer.vue，issue gofer-5inz）：看板卡片 / 列表行点开后
// 右侧抽屉里要有标题、状态、依赖、验收标准、关联 job（可跳转）、备注、待办评论与关联 job
// 的事件，常用操作（改状态、派发 / 重派）在抽屉里；todo 为空即关闭（Teleport 里什么都不渲染）。
// 用户反馈后的收敛：计划级交接说明不再进抽屉（计划详情页已有），事件是「关联 job 的事件」
// 不是待办自己的、默认折叠且展开前不请求，备注按 markdown 渲染（裸 URL 可点、换行保留）。
//
// SSR 渲染抽屉内容（Teleport 的部分在 ctx.teleports.body）；网络三个入口（评论 / 事件）
// 用 vi.mock 换掉，只断言接线与渲染。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import TodoDrawer from './TodoDrawer.vue'
import { setLocale } from '../store/locale'
import type { Todo } from '../api/types'

// 测试环境没有 DOM，MarkdownBlock 的 DOMPurify 需要 window：把 sanitize 当透传。
vi.mock('dompurify', () => ({ default: { sanitize: (html: string) => html } }))

const api = vi.hoisted(() => ({
  listEvents: vi.fn(async () => ({
    events: [{ seq: 2, type: 'job.terminal', at: 1_700_000_100, detail: '{"status":"done","exit_code":0}' }],
  })),
  listComments: vi.fn(async () => [] as unknown[]),
  postComment: vi.fn(async () => ({ id: 'c1' })),
}))

vi.mock('../api/client', () => ({
  listEvents: (...args: unknown[]) => api.listEvents(...(args as [])),
  listComments: (...args: unknown[]) => api.listComments(...(args as [])),
  postComment: (...args: unknown[]) => api.postComment(...(args as [])),
}))

const listEvents = api.listEvents

const source =
  (Object.values(import.meta.glob('./TodoDrawer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string) ?? ''

const todo: Todo = {
  todo_id: 't-1',
  plan_id: 'p-1',
  title: '补齐看板空列宽度',
  done: false,
  status: 'doing',
  assignee: 'omp',
  jobs: [{ id: 'job-abcdefghijkl', status: 'needs_review', duration_sec: 42, pr_url: 'https://example.test/pr/1' }],
  after: ['t-0'],
  auto: false,
  acceptance: '空列宽约普通列三分之一',
  scope: ['web/src/components/PlanBoard.vue'],
  verify: ['npx', 'vitest', 'run'],
  review: true,
  project: 'demo',
  template: 'plan-todo',
  runner: 'local',
  cwd: 'web',
  timeout_sec: 1800,
  note: '按 issue 第 2 条做',
  created_at: 1_700_000_000,
  started_at: 1_700_000_050,
  updated_at: 1_700_000_200,
}

const dep: Todo = {
  todo_id: 't-0',
  plan_id: 'p-1',
  title: '先读设计稿',
  done: true,
  status: 'done',
  created_at: 1_699_000_000,
  updated_at: 1_699_000_100,
}

async function render(props: Record<string, unknown>): Promise<string> {
  const ctx: { teleports?: Record<string, string> } = {}
  const app = createSSRApp({ render: () => h(TodoDrawer, props as never) })
  await renderToString(app, ctx)
  return ctx.teleports?.body ?? ''
}

afterEach(() => {
  setLocale('zh-CN')
  listEvents.mockClear()
})

describe('待办详情抽屉的内容', () => {
  it('stays closed (renders nothing) when no todo is selected', async () => {
    const html = await render({ todo: null, todos: [], jobs: [], agents: [] })
    expect(html).not.toContain('data-test="todo-drawer"')
    expect(listEvents).not.toHaveBeenCalled()
  })

  it('shows title, status, deps, acceptance, dispatch fields, jobs and note', async () => {
    setLocale('zh-CN')
    const html = await render({ todo, todos: [todo, dep], jobs: [], agents: [] })
    expect(html).toContain('data-test="todo-drawer-body"')
    // 标题是业务内容：原样显示，不做翻译
    expect(html).toContain('data-test="todo-drawer-title"')
    expect(html).toContain('补齐看板空列宽度')
    expect(html).toContain('待办详情')
    // 状态：pill（原始状态值，与 job/看板一致）+ 可改状态的下拉
    expect(html).toContain('data-test="todo-drawer-status"')
    expect(html).toContain('data-testid="status-pill"')
    expect(html).toContain('doing')
    // 依赖：同 plan 条目的标题 + 完成标记 + 手动标记
    expect(html).toContain('data-test="todo-drawer-deps"')
    expect(html).toContain('先读设计稿')
    expect(html).toContain('依赖已全部完成')
    expect(html).toContain('手动')
    // 验收标准 / 范围 / 验收命令 / 人验收
    expect(html).toContain('data-test="todo-drawer-acceptance"')
    expect(html).toContain('空列宽约普通列三分之一')
    expect(html).toContain('web/src/components/PlanBoard.vue')
    expect(html).toContain('npx vitest run')
    expect(html).toContain('要人验收')
    // 派发字段
    expect(html).toContain('data-test="todo-drawer-dispatch"')
    expect(html).toContain('omp')
    expect(html).toContain('plan-todo')
    expect(html).toContain('1800s')
    // 关联 job：状态 pill + 可跳转 + PR
    expect(html).toContain('data-test="todo-drawer-jobs"')
    expect(html).toContain('job-abcdefghijkl')
    expect(html).toContain('https://example.test/pr/1')
    // 备注 / 时间
    expect(html).toContain('data-test="todo-drawer-note"')
    expect(html).toContain('按 issue 第 2 条做')
    expect(html).toContain('data-test="todo-drawer-times"')
    // 评论线程（scope=todo）；事件节标题是「关联任务的事件」，默认折叠
    expect(html).toContain('data-test="todo-drawer-comments"')
    expect(html).toContain('待办评论')
    expect(html).toContain('data-test="todo-drawer-events"')
    expect(html).toContain('关联任务的事件')
  })

  it('leaves the plan-level handoff note out of the drawer', async () => {
    setLocale('zh-CN')
    const html = await render({ todo, todos: [todo, dep], jobs: [], agents: [] })
    // 交接说明是整个计划的，计划详情页已有；抽屉里不该再出现一遍（也不该再有这个 prop）。
    expect(html).not.toContain('data-test="todo-drawer-handoff"')
    expect(source).not.toContain('handoff')
    // 连类型也不再引用：没有这个 prop 的痕迹
    expect(source).not.toContain('PlanHandoff')
  })

  it('keeps the linked-job events collapsed and does not fetch them up front', async () => {
    setLocale('zh-CN')
    const html = await render({ todo, todos: [todo, dep], jobs: [], agents: [] })
    // 折叠态：标题在、展开标记 aria-expanded=false，列表内容一概没有
    expect(html).toContain('data-test="todo-drawer-events-toggle"')
    expect(html).toContain('aria-expanded="false"')
    expect(html).not.toContain('没有关联 job 的事件')
    expect(html).not.toContain('加载事件')
    // 打开抽屉不预取：展开时才调 loadEvents
    expect(listEvents).not.toHaveBeenCalled()
    expect(source).toContain('if (eventsOpen.value && !eventsLoaded.value) void loadEvents()')
  })

  it('hides the events section entirely when no job is linked', async () => {
    setLocale('zh-CN')
    const bare: Todo = {
      todo_id: 't-3',
      plan_id: 'p-1',
      title: '没跑过',
      done: false,
      status: 'pending',
      created_at: 1_700_000_000,
      updated_at: 1_700_000_000,
    }
    const html = await render({ todo: bare, todos: [bare], jobs: [], agents: [] })
    expect(html).not.toContain('data-test="todo-drawer-events"')
    expect(listEvents).not.toHaveBeenCalled()
  })

  it('renders the note as markdown: bare URLs get a new-tab target and line breaks survive', async () => {
    setLocale('zh-CN')
    const noted: Todo = {
      ...todo,
      note: '复现：https://bugs.example.test/t/9\n第二条：已经修了',
    }
    const html = await render({ todo: noted, todos: [noted, dep], jobs: [], agents: [] })
    const note = html.slice(html.indexOf('data-test="todo-drawer-note"'), html.indexOf('data-test="todo-drawer-times"'))
    expect(note).toContain('href="https://bugs.example.test/t/9"')
    expect(note).toContain('target="_blank"')
    expect(note).toContain('rel="noopener noreferrer"')
    // 多行备注（--append-note 每次追加一行）不能被并成一段
    expect(note).toContain('<br>')
    expect(note).toContain('第二条：已经修了')
  })

  it('keeps the empty states when there is nothing to show', async () => {
    setLocale('zh-CN')
    const bare: Todo = {
      todo_id: 't-2',
      plan_id: 'p-1',
      title: '空条目',
      done: false,
      status: 'pending',
      created_at: 1_700_000_000,
      updated_at: 1_700_000_000,
    }
    const html = await render({ todo: bare, todos: [bare], jobs: [], agents: [] })
    expect(html).toContain('没有前序条目（根节点）')
    expect(html).toContain('没有声明验收标准')
    expect(html).toContain('还没有执行过')
  })

  it('disables the writes and the comment box without write permission', async () => {
    setLocale('zh-CN')
    const html = await render({
      todo,
      todos: [todo, dep],
      jobs: [],
      agents: [],
      writeBlock: '只能管理自己的计划',
    })
    expect(html).toContain('data-test="todo-drawer-readonly"')
    expect(html).toContain('只能管理自己的计划')
    expect(html).toContain('data-test="todo-drawer-comments"')
    expect(html).toContain('data-test="cm-readonly"')
  })

  it('renders the labels in English after a locale switch', async () => {
    setLocale('en-US')
    const html = await render({ todo, todos: [todo, dep], jobs: [], agents: [] })
    expect(html).toContain('Todo details')
    expect(html).toContain('Acceptance criteria')
    expect(html).toContain('Related jobs')
    expect(html).toContain('Dispatch again')
    expect(html).toContain('Todo comments')
    expect(html).toContain('Events of linked jobs')
    expect(html).not.toContain('待办详情')
  })
})

describe('抽屉的接线', () => {
  it('sends the status and dispatch intents back to the host page', () => {
    expect(source).toContain("emit('status', todo, status)")
    expect(source).toContain("emit('dispatch', todo, agent)")
    expect(source).toContain("emit('open-job', j.id)")
    // 没人认领时先选 agent，选不到就不发（与看板拖到 ready 同一条规则）
    expect(source).toContain('if (!todo.assignee && !agent) return')
  })

  it('reuses the shared drawer shell and the todo comment thread', () => {
    expect(source).toContain("import Drawer from '../ui/Drawer.vue'")
    expect(source).toContain(':scope="\'todo\'"')
    expect(source).toContain("from 'vue'")
  })
})
