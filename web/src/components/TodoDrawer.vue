<script setup lang="ts">
// 待办详情抽屉（issue gofer-5inz）：看板卡片 / 列表行点开右侧抽屉，把这一条待办的
// 全部信息放在一处——标题、状态、依赖、验收标准、派发字段、关联 job（可跳转）、备注、
// 计划交接说明、评论与事件；常用操作（改状态、派发 / 重派）也在抽屉里，走的还是列表行
// 那两条路径（宿主页面 onTodoStatus / dispatchTodo），本组件只发意图、不改数据。
//  - 抽屉外壳统一用 ui/Drawer（§8）；评论复用 CommentThread（scope=todo，接口早已存在）。
//  - 事件没有 todo 级的接口：拉关联 job 的事件合并展示（最多看最近 5 个 job），空则显示说明。
//  - 写操作受宿主页面的 plan 写权限门控（writeBlock 非空 = 只读，按钮禁用并给出原因）。
import { computed, ref, watch } from 'vue'
import { t } from '../i18n'
import Drawer from '../ui/Drawer.vue'
import Icon from '../ui/Icon.vue'
import StatusPill from '../ui/StatusPill.vue'
import MarkdownBlock from './MarkdownBlock.vue'
import CommentThread from './CommentThread.vue'
import { listEvents } from '../api/client'
import { fmtDateTime, jobDurationSec } from '../api/time'
import { formatRelative } from '../utils/format'
import { eventDetailText, eventIcon, eventLabel } from '../utils/eventMeta'
import { depsSatisfied } from '../utils/planBoard'
import { usageBadge, verifyLabel } from '../utils/jobOutcome'
import type { AgentInfo, Job, JobEvent, PlanHandoff, Todo, TodoStatus } from '../api/types'

const props = defineProps<{
  todo: Todo | null
  // 同 plan 的其它条目：依赖与接续要显示对方的标题，不是裸 id。
  todos: Todo[]
  // plan.jobs：卡片上的用量 / verify 只有完整 job 才有（todo.jobs 是轻量投影）。
  jobs: Job[]
  // 派发时给没 assignee 的条目选 agent（与看板「拖到 ready 先选 agent」同一份候选）。
  agents: AgentInfo[]
  // 计划当前的交接说明（plan 详情里已经取到了，这里只展示，不另发请求）。
  handoff: PlanHandoff | null
  // 非空 = 没有写权限（自己的 plan 之外），文案即禁用原因。
  writeBlock?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'status', todo: Todo, status: TodoStatus): void
  (e: 'dispatch', todo: Todo, agent: string): void
  (e: 'open-job', id: string): void
}>()

const TODO_STATUSES: TodoStatus[] = ['pending', 'ready', 'doing', 'done', 'skipped']

const open = computed({
  get: () => !!props.todo,
  set: (v: boolean) => {
    if (!v) emit('close')
  },
})

const jobById = computed(() => new Map(props.jobs.map((j) => [j.id, j])))

// related：这一条待办挂接的 job（服务端新→旧，最多 10 条）；只有手工绑定的 job_id 时按它兜底。
const related = computed(() => {
  const todo = props.todo
  if (!todo) return []
  const list = (todo.jobs ?? []).map((j) => ({
    id: j.id,
    status: j.status as string,
    durationSec: j.duration_sec ?? 0,
    prUrl: j.pr_url ?? '',
    usage: '',
    verify: '',
  }))
  if (list.length === 0 && todo.job_id) {
    const full = jobById.value.get(todo.job_id)
    list.push({
      id: todo.job_id,
      status: full?.status ?? '',
      durationSec: full ? jobDurationSec(full) ?? 0 : 0,
      prUrl: full?.delivery?.pr_url ?? '',
      usage: '',
      verify: '',
    })
  }
  // 完整 job 才有用量与 verify 结果：有就补上，没有就省略。
  return list.map((row) => {
    const full = jobById.value.get(row.id)
    const u = full?.usage
    const usage = u && ((u.total_tokens ?? 0) > 0 || (u.cost_usd ?? 0) > 0) ? usageBadge(u) : ''
    return { ...row, usage, verify: verifyLabel(full?.verify) }
  })
})

const after = computed(() => {
  const ids = props.todo?.after ?? []
  return ids.map((id) => {
    const dep = props.todos.find((x) => x.todo_id === id)
    return {
      id,
      title: dep?.title || id,
      status: dep?.status ?? '',
      ok: !!dep && (dep.status === 'done' || dep.status === 'skipped'),
    }
  })
})

const depsOk = computed(() => (props.todo ? depsSatisfied(props.todo, props.todos) : true))

// titleOf：after / continue_from 里的 id → 条目标题（找不到就原样显示 id）。
function titleOf(id: string): string {
  return props.todos.find((x) => x.todo_id === id)?.title || id
}

const continuedBy = computed(() => (props.todo?.continued_by ? titleOf(props.todo.continued_by) : ''))

// —— 关联 job 的事件（没有 todo 级接口，合并最近几个 job 的事件） ——
const EVENT_JOBS = 5
const EVENT_SHOWN = 20
const events = ref<Array<JobEvent & { jobId: string }>>([])
const eventsLoading = ref(false)
const eventsError = ref('')

async function loadEvents(): Promise<void> {
  const ids = related.value.slice(0, EVENT_JOBS).map((r) => r.id)
  events.value = []
  eventsError.value = ''
  if (ids.length === 0) return
  eventsLoading.value = true
  try {
    const pages = await Promise.all(
      ids.map(async (id) => {
        try {
          const res = await listEvents(id)
          return (res.events ?? []).map((ev) => ({ ...ev, jobId: id }))
        } catch {
          return [] // 单个 job 拉不到不影响其余
        }
      }),
    )
    events.value = pages
      .flat()
      .sort((a, b) => b.at - a.at || b.seq - a.seq)
      .slice(0, EVENT_SHOWN)
  } catch (e) {
    eventsError.value = e instanceof Error ? e.message : String(e)
  } finally {
    eventsLoading.value = false
  }
}

// 换条目 / 打开抽屉时重拉事件（卡片与列表共用这一个抽屉）。
watch(() => props.todo?.todo_id, () => {
  void loadEvents()
}, { immediate: true })

function onStatusChange(ev: Event): void {
  const todo = props.todo
  const status = (ev.target as HTMLSelectElement).value as TodoStatus
  if (!todo || props.writeBlock) return
  emit('status', todo, status)
}

// 派发：有 assignee 直接发（重派同一路径）；没人认领先在这里选 agent。
const pickAgent = ref('')
watch(() => props.todo?.todo_id, () => { pickAgent.value = '' })

function onDispatch(): void {
  const todo = props.todo
  if (!todo || props.writeBlock) return
  const agent = todo.assignee ? '' : pickAgent.value.trim()
  if (!todo.assignee && !agent) return
  emit('dispatch', todo, agent)
}

const canDispatch = computed(() => {
  const todo = props.todo
  if (!todo || props.writeBlock) return false
  return !!todo.assignee || !!pickAgent.value.trim()
})
</script>

<template>
  <Drawer
    v-model="open"
    width="480px"
    :label="t('plans.todo.drawer')"
    data-test="todo-drawer"
  >
    <div v-if="todo" class="td" data-test="todo-drawer-body">
      <h3 class="td-title" data-test="todo-drawer-title">{{ todo.title }}</h3>
      <p class="td-sub mono">
        <StatusPill :status="todo.status" />
        <span v-if="todo.assignee" class="td-assignee">{{ todo.assignee }}</span>
        <span v-else class="td-muted">{{ t('plans.board.unassigned') }}</span>
        <span v-if="todo.auto === false" class="td-flag">{{ t('plans.board.manual') }}</span>
        <span class="td-muted">{{ t('plans.todo.updated', { when: formatRelative(todo.updated_at) }) }}</span>
      </p>

      <!-- 常用操作：改状态 + 派发 / 重派（走宿主页面已有的两条路径）。 -->
      <div class="td-ops" data-test="todo-drawer-ops">
        <label class="td-op-field">
          <span class="td-k mono">{{ t('plans.todo.status') }}</span>
          <select
            class="td-input mono"
            :value="todo.status"
            :disabled="!!writeBlock"
            :title="writeBlock || t('plans.todo.statusHint')"
            data-test="todo-drawer-status"
            @change="onStatusChange"
          >
            <option v-for="st in TODO_STATUSES" :key="st" :value="st">{{ st }}</option>
          </select>
        </label>
        <select
          v-if="!todo.assignee"
          v-model="pickAgent"
          class="td-input mono"
          :disabled="!!writeBlock"
          data-test="todo-drawer-agent"
        >
          <option value="">{{ t('plans.detail.agentPlaceholder') }}</option>
          <option v-for="a in agents" :key="a.key" :value="a.key">{{ a.key }} · {{ a.type }}</option>
        </select>
        <button
          class="btn td-btn"
          type="button"
          :disabled="!canDispatch"
          :title="writeBlock || undefined"
          data-test="todo-drawer-dispatch"
          @click="onDispatch"
        >
          {{ todo.assignee ? t('plans.todo.redispatch') : t('plans.detail.dispatch') }}
        </button>
      </div>
      <p v-if="writeBlock" class="td-muted mono" data-test="todo-drawer-readonly">{{ writeBlock }}</p>
      <p v-if="todo.dispatch_error" class="td-error mono" data-test="todo-drawer-dispatch-error">
        {{ t('plans.detail.dispatchFailed', { err: todo.dispatch_error }) }}
      </p>

      <section class="td-sec" data-test="todo-drawer-deps">
        <h4 class="td-h mono">{{ t('plans.todo.deps') }}</h4>
        <ul v-if="after.length > 0" class="td-list">
          <li v-for="d in after" :key="d.id" class="td-li mono">
            <span class="td-dep" :class="{ 'td-dep--ok': d.ok }">{{ d.ok ? '✓' : '…' }}</span>
            <span class="td-li-text">{{ d.title }}</span>
          </li>
        </ul>
        <p v-else class="td-muted mono">{{ t('plans.todo.depsNone') }}</p>
        <p class="td-muted mono">
          {{ depsOk ? t('plans.todo.depsOk') : t('plans.todo.depsWaiting') }}
          <template v-if="todo.auto === false"> · {{ t('plans.board.manualTitle') }}</template>
        </p>
        <!-- 接续（gofer-rnp3）：本项接在前序的 agent 会话里跑，或已被后继接走。 -->
        <template v-if="todo.continue_from || continuedBy">
          <p v-if="todo.continue_from" class="td-muted mono">
            {{ t('plans.todo.continueFrom', { title: titleOf(todo.continue_from) }) }}
          </p>
          <p v-if="continuedBy" class="td-muted mono">{{ t('plans.todo.continuedBy', { title: continuedBy }) }}</p>
          <p v-if="todo.continue_note" class="td-muted mono">{{ todo.continue_note }}</p>
        </template>
      </section>

      <section class="td-sec" data-test="todo-drawer-acceptance">
        <h4 class="td-h mono">{{ t('plans.todo.acceptance') }}</h4>
        <p v-if="todo.acceptance" class="td-pre mono">{{ todo.acceptance }}</p>
        <p v-else class="td-muted mono">{{ t('plans.todo.acceptanceNone') }}</p>
        <dl class="td-kv mono">
          <div v-if="todo.scope?.length" class="td-kv-row">
            <dt>{{ t('plans.todo.scope') }}</dt>
            <dd>{{ todo.scope.join(', ') }}</dd>
          </div>
          <div v-if="todo.verify?.length" class="td-kv-row">
            <dt>{{ t('plans.todo.verify') }}</dt>
            <dd>{{ todo.verify.join(' ') }}</dd>
          </div>
          <div class="td-kv-row">
            <dt>{{ t('plans.todo.review') }}</dt>
            <dd>{{ todo.review ? t('plans.todo.reviewRequired') : t('plans.todo.reviewNotRequired') }}</dd>
          </div>
        </dl>
      </section>

      <section class="td-sec" data-test="todo-drawer-dispatch">
        <h4 class="td-h mono">{{ t('plans.todo.dispatchFields') }}</h4>
        <dl class="td-kv mono">
          <div class="td-kv-row">
            <dt>{{ t('plans.todo.assignee') }}</dt>
            <dd>{{ todo.assignee || '—' }}</dd>
          </div>
          <div v-if="todo.project" class="td-kv-row">
            <dt>{{ t('plans.todo.project') }}</dt>
            <dd>{{ todo.project }}</dd>
          </div>
          <div v-if="todo.template" class="td-kv-row">
            <dt>{{ t('plans.todo.template') }}</dt>
            <dd>{{ todo.template }}</dd>
          </div>
          <div v-if="todo.runner" class="td-kv-row">
            <dt>{{ t('plans.todo.runner') }}</dt>
            <dd>{{ todo.runner }}</dd>
          </div>
          <div v-if="todo.cwd" class="td-kv-row">
            <dt>{{ t('plans.todo.cwd') }}</dt>
            <dd class="td-break">{{ todo.cwd }}</dd>
          </div>
          <div v-if="todo.timeout_sec" class="td-kv-row">
            <dt>{{ t('plans.todo.timeout') }}</dt>
            <dd>{{ todo.timeout_sec }}s</dd>
          </div>
        </dl>
      </section>

      <section class="td-sec" data-test="todo-drawer-jobs">
        <h4 class="td-h mono">{{ t('plans.todo.jobs') }}</h4>
        <ul v-if="related.length > 0" class="td-list">
          <li v-for="j in related" :key="j.id" class="td-li">
            <StatusPill v-if="j.status" :status="j.status" />
            <button class="td-link mono" type="button" @click="emit('open-job', j.id)">
              {{ j.id }} &rarr;
            </button>
            <span v-if="j.usage" class="td-muted mono">{{ j.usage }}</span>
            <span v-if="j.verify" class="td-muted mono">{{ j.verify }}</span>
            <span v-if="j.durationSec" class="td-muted mono">{{ j.durationSec }}s</span>
            <a
              v-if="j.prUrl"
              class="td-link mono"
              :href="j.prUrl"
              target="_blank"
              rel="noopener noreferrer"
            >PR &#8599;</a>
          </li>
        </ul>
        <p v-else class="td-muted mono">{{ t('plans.todo.jobsNone') }}</p>
      </section>

      <section v-if="todo.note" class="td-sec" data-test="todo-drawer-note">
        <h4 class="td-h mono">{{ t('plans.todo.note') }}</h4>
        <p class="td-pre mono">{{ todo.note }}</p>
      </section>

      <section class="td-sec" data-test="todo-drawer-times">
        <h4 class="td-h mono">{{ t('plans.todo.times') }}</h4>
        <dl class="td-kv mono">
          <div class="td-kv-row">
            <dt>{{ t('plans.todo.created') }}</dt>
            <dd>{{ fmtDateTime(todo.created_at) }}</dd>
          </div>
          <div v-if="todo.started_at" class="td-kv-row">
            <dt>{{ t('plans.todo.started') }}</dt>
            <dd>{{ fmtDateTime(todo.started_at) }}</dd>
          </div>
          <div v-if="todo.done_at" class="td-kv-row">
            <dt>{{ t('plans.todo.done') }}</dt>
            <dd>{{ fmtDateTime(todo.done_at) }}</dd>
          </div>
        </dl>
      </section>

      <!-- 计划当前的交接说明：待办是链上的一环，接手人要能看到计划层面的交代。 -->
      <section class="td-sec" data-test="todo-drawer-handoff">
        <h4 class="td-h mono">
          {{ t('plans.todo.handoff') }}
          <span v-if="handoff" class="td-muted">v{{ handoff.version }} · {{ handoff.by }}</span>
        </h4>
        <MarkdownBlock v-if="handoff?.body" :text="handoff.body" />
        <p v-else class="td-muted mono">{{ t('plans.todo.handoffNone') }}</p>
      </section>

      <section class="td-sec" data-test="todo-drawer-comments">
        <h4 class="td-h mono">{{ t('plans.todo.comments') }}</h4>
        <CommentThread
          :scope="'todo'"
          :id="todo.todo_id"
          :placeholder="t('plans.todo.commentPlaceholder')"
          :readonly="!!writeBlock"
        />
      </section>

      <section class="td-sec" data-test="todo-drawer-events">
        <h4 class="td-h mono">{{ t('plans.todo.events') }}</h4>
        <p v-if="eventsLoading" class="td-muted mono">{{ t('plans.todo.eventsLoading') }}</p>
        <p v-else-if="eventsError" class="td-error mono">{{ eventsError }}</p>
        <ul v-else-if="events.length > 0" class="td-list">
          <li v-for="ev in events" :key="`${ev.jobId}-${ev.seq}`" class="td-li mono">
            <Icon class="td-ev-icon" :icon="eventIcon(ev.type)" size="sm" />
            <span class="td-li-text">{{ eventLabel(ev.type) }}</span>
            <span v-if="eventDetailText(ev)" class="td-muted">{{ eventDetailText(ev) }}</span>
            <span class="td-muted">{{ fmtDateTime(ev.at) }}</span>
          </li>
        </ul>
        <p v-else class="td-muted mono">{{ t('plans.todo.eventsNone') }}</p>
      </section>
    </div>
  </Drawer>
</template>

<style scoped>
.td {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
}
.td-title {
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--ink);
  word-break: break-word;
}
.td-sub {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s1);
  margin: 0;
  font-size: var(--fs-sm);
}
.td-assignee {
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 6px;
}
.td-flag {
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 6px;
}
.td-muted {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  word-break: break-word;
}
.td-error {
  margin: 0;
  color: var(--fail);
  font-size: var(--fs-sm);
  word-break: break-word;
}
.td-ops {
  display: flex;
  align-items: flex-end;
  flex-wrap: wrap;
  gap: var(--s2);
}
.td-op-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.td-k {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.td-input {
  min-height: 32px;
  background: var(--sunken);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 4px 8px;
  font: inherit;
  font-size: var(--fs-sm);
}
.td-btn {
  min-height: 32px;
}
.td-sec {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  padding-top: var(--s2);
  border-top: 1px solid var(--line);
}
.td-h {
  display: flex;
  align-items: baseline;
  gap: var(--s1);
  margin: 0;
  color: var(--ink-2);
  font-size: var(--fs-sm);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.td-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.td-li {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: var(--fs-sm);
  color: var(--ink);
}
.td-li-text {
  min-width: 0;
  word-break: break-word;
}
.td-dep {
  color: var(--ink-3);
}
.td-dep--ok {
  color: var(--done);
}
.td-kv {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin: 0;
}
.td-kv-row {
  display: flex;
  gap: var(--s2);
  font-size: var(--fs-sm);
}
.td-kv-row dt {
  flex: 0 0 auto;
  min-width: 88px;
  color: var(--ink-3);
}
.td-kv-row dd {
  margin: 0;
  min-width: 0;
  color: var(--ink);
  word-break: break-word;
}
.td-break {
  word-break: break-all;
}
.td-pre {
  margin: 0;
  color: var(--ink);
  font-size: var(--fs-sm);
  white-space: pre-wrap;
  word-break: break-word;
}
.td-link {
  background: none;
  border: none;
  padding: 0;
  color: var(--accent);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.td-link:hover {
  text-decoration: underline;
}
.td-ev-icon {
  color: var(--ink-3);
}
</style>
