<script setup lang="ts">
// 计划看板（WEB-10）：五列 + 原生 HTML5 拖拽派发（决策 2：不引拖拽库）。
//  - 本组件只表达「拖拽意图」：落点判定走 utils/planBoard 的纯函数，真正的 PATCH、乐观
//    更新与失败回滚由 PlanDetail 做（它持有 plan 详情这一份数据，列表/看板同源、同一轮询）。
//  - 无 assignee 的条目拖到 ready 先弹 agent 选择（与列表页「派发」同一条 PATCH）；done /
//    skipped 落点先弹确认（人工标记是终态操作）。
//  - 拖拽期间 emit('drag-active', true) 让 PlanDetail 暂停轮询覆盖：一次重渲染会把正在
//    拖的卡片换掉，拖拽就断了。
//  - doing / needs_review 两列由服务端驱动，不可拖入（拖动时它们显示禁用态）。
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import StatusBadge from './StatusBadge.vue'
import { usageBadge, verifyClass, verifyLabel } from '../utils/jobOutcome'
import {
  BOARD_COLUMNS, allowedMove, columnOf, columnTodos, depsSatisfied, dropTargets,
} from '../utils/planBoard'
import type { BoardColumn, BoardTarget } from '../utils/planBoard'
import type { AgentInfo, Job, JobStatus, Todo, TodoStatus } from '../api/types'

const props = defineProps<{
  todos: Todo[]
  // plan.jobs：卡片的用量与 verify 结果只有完整 job 才有（todo.jobs 是轻量投影）。
  jobs: Job[]
  agents: AgentInfo[]
  // 正在写库的 todo id（乐观更新进行中）：该卡片禁拖，避免同一张卡并发两次 PATCH。
  busyIds: Set<string>
}>()

const emit = defineEmits<{
  (e: 'move', payload: { todo: Todo; status: TodoStatus; assignee?: string }): void
  (e: 'drag-active', active: boolean): void
}>()

const router = useRouter()

// 列头文案用状态名（与列表页/CLI 同一套词），中文解释放 title —— 看板不发明新术语。
const COLUMN_TITLES: Record<BoardColumn, string> = {
  pending: '待办：不触发派发，拖到 ready 即派发',
  ready: '已布防：有 assignee 且无活跃 job 时服务端立刻派发',
  doing: '进行中（服务端驱动，不能拖入）',
  needs_review: '待验收：最近一次 job 在等人裁决',
  done: '完成（skipped 折叠在本列尾，灰色）',
}

interface CardJob {
  id: string
  status: JobStatus
  usage: string
  verify: string
  verifyCls: string
}

interface BoardCard {
  todo: Todo
  job: CardJob | null
  deps: number
  depsOk: boolean
  depsTitle: string
  manual: boolean
  skipped: boolean
}

const jobById = computed(() => new Map(props.jobs.map((j) => [j.id, j])))

// 最近一次 job：todo.jobs[0]（服务端新→旧，最多 10 条）；只有手工绑定的 job_id 时按
// plan.jobs 兜底。用量/verify 一律取完整 job（投影里没有），没报数字就整块省略。
function cardJob(t: Todo): CardJob | null {
  const latest = t.jobs && t.jobs.length > 0 ? t.jobs[0] : undefined
  const id = latest ? latest.id : t.job_id ?? ''
  const full = id ? jobById.value.get(id) : undefined
  const status = latest ? latest.status : full?.status
  if (!id || !status) {
    return null
  }
  const u = full?.usage
  return {
    id,
    status,
    usage: (u?.total_tokens ?? 0) > 0 || (u?.cost_usd ?? 0) > 0 ? usageBadge(u) : '',
    verify: verifyLabel(full?.verify),
    verifyCls: verifyClass(full?.verify),
  }
}

function decorate(t: Todo): BoardCard {
  const after = t.after ?? []
  return {
    todo: t,
    job: cardJob(t),
    deps: after.length,
    depsOk: depsSatisfied(t, props.todos),
    depsTitle:
      after.length > 0
        ? `依赖：\n${after.map((id) => props.todos.find((x) => x.todo_id === id)?.title ?? id).join('\n')}`
        : '',
    manual: t.auto === false,
    skipped: t.status === 'skipped',
  }
}

const columns = computed(() =>
  BOARD_COLUMNS.map((key) => ({
    key,
    title: COLUMN_TITLES[key],
    cards: columnTodos(props.todos, key).map(decorate),
  })),
)

// —— 拖拽 ——
const dragging = ref('')
const dragFrom = ref<BoardColumn>('pending')
const overCol = ref<BoardColumn | ''>('')
const overTarget = ref<BoardTarget | ''>('')

const dragTodo = computed<Todo | null>(
  () => props.todos.find((t) => t.todo_id === dragging.value) ?? null,
)

function targetAllowed(target: BoardTarget): boolean {
  const t = dragTodo.value
  return !!t && allowedMove(dragFrom.value, target, t)
}

function columnAllowed(col: BoardColumn): boolean {
  return dropTargets(col).some((target) => targetAllowed(target))
}

function resetDrag(): void {
  dragging.value = ''
  overCol.value = ''
  overTarget.value = ''
  emit('drag-active', false)
}

function onDragStart(t: Todo, ev: DragEvent): void {
  if (props.busyIds.has(t.todo_id)) {
    ev.preventDefault()
    return
  }
  dragging.value = t.todo_id
  dragFrom.value = columnOf(t)
  overCol.value = ''
  overTarget.value = ''
  emit('drag-active', true)
  if (ev.dataTransfer) {
    ev.dataTransfer.effectAllowed = 'move'
    ev.dataTransfer.setData('text/plain', t.todo_id)
  }
}

// 单落点列（pending/ready）由列体接收：多落点的 done 列交给下面的落点块，故这里只在
// 恰好一个落点时 preventDefault（不 preventDefault = 浏览器判为不可放置）。
function onDragOverCol(col: BoardColumn, ev: DragEvent): void {
  const targets = dropTargets(col)
  if (targets.length !== 1 || !targetAllowed(targets[0])) {
    return
  }
  ev.preventDefault()
  if (ev.dataTransfer) {
    ev.dataTransfer.dropEffect = 'move'
  }
  overCol.value = col
}

function onDropCol(col: BoardColumn, ev: DragEvent): void {
  const targets = dropTargets(col)
  if (targets.length !== 1) {
    return
  }
  ev.preventDefault()
  commit(targets[0])
}

function onDragOverTarget(target: BoardTarget, ev: DragEvent): void {
  if (!targetAllowed(target)) {
    return
  }
  ev.preventDefault()
  if (ev.dataTransfer) {
    ev.dataTransfer.dropEffect = 'move'
  }
  overTarget.value = target
}

function onDropTarget(target: BoardTarget, ev: DragEvent): void {
  ev.preventDefault()
  commit(target)
}

// 落点确认：pending/ready 直接发；ready 且没人认领先选 agent；done/skipped 先弹确认。
function commit(target: BoardTarget): void {
  const t = dragTodo.value
  const from = dragFrom.value
  resetDrag()
  if (!t || !allowedMove(from, target, t)) {
    return
  }
  if (target === 'pending' || target === 'ready') {
    if (target === 'ready' && !t.assignee) {
      pickerTodo.value = t
      pickAgent.value = ''
      return
    }
    emit('move', { todo: t, status: target })
    return
  }
  if (target === 'done' || target === 'skipped') {
    confirmTarget.value = { todo: t, status: target }
  }
}

// —— 两个弹层：选 agent 派发 / 确认人工标记 ——
const pickerTodo = ref<Todo | null>(null)
const pickAgent = ref('')
const confirmTarget = ref<{ todo: Todo; status: 'done' | 'skipped' } | null>(null)

function confirmPick(): void {
  const t = pickerTodo.value
  const agent = pickAgent.value.trim()
  if (!t || !agent) {
    return
  }
  pickerTodo.value = null
  emit('move', { todo: t, status: 'ready', assignee: agent })
}

function confirmMark(): void {
  const c = confirmTarget.value
  if (!c) {
    return
  }
  confirmTarget.value = null
  emit('move', { todo: c.todo, status: c.status })
}

function openJob(id: string): void {
  void router.push(`/jobs/${encodeURIComponent(id)}`)
}

function shortId(id: string): string {
  return id.length > 14 ? `…${id.slice(-10)}` : id
}
</script>

<template>
  <div class="board-wrap">
    <div class="board">
      <section
        v-for="c in columns"
        :key="c.key"
        class="col"
        :class="{
          'col--disabled': dragging !== '' && !columnAllowed(c.key),
          'col--over':
            overCol === c.key ||
            (overTarget !== '' && dropTargets(c.key).includes(overTarget)),
        }"
      >
        <header class="col-head mono" :title="c.title">
          <span class="col-name">{{ c.key }}</span>
          <span class="col-count">{{ c.cards.length }}</span>
        </header>
        <div
          class="col-body"
          @dragover="onDragOverCol(c.key, $event)"
          @drop="onDropCol(c.key, $event)"
        >
          <!-- done 列两个落点（完成 / 跳过）：拖拽时才出现，点哪儿决定 PATCH 哪个 status。 -->
          <div
            v-if="dragging !== '' && dropTargets(c.key).length > 1"
            class="zones"
          >
            <div
              v-for="z in dropTargets(c.key)"
              :key="z"
              class="zone mono"
              :class="{ 'zone--over': overTarget === z }"
              @dragover.stop="onDragOverTarget(z, $event)"
              @drop.stop="onDropTarget(z, $event)"
            >
              {{ z }}
            </div>
          </div>
          <article
            v-for="card in c.cards"
            :key="card.todo.todo_id"
            class="card"
            :class="{
              'card--dragging': dragging === card.todo.todo_id,
              'card--busy': busyIds.has(card.todo.todo_id),
              'card--skipped': card.skipped,
            }"
            draggable="true"
            @dragstart="onDragStart(card.todo, $event)"
            @dragend="resetDrag"
          >
            <div class="card-top">
              <span class="card-title" :title="card.todo.title">{{ card.todo.title }}</span>
              <span class="card-flags">
                <span
                  v-if="card.deps > 0"
                  class="dep mono"
                  :class="{ 'dep--ok': card.depsOk }"
                  :title="card.depsTitle"
                >
                  ⛓ {{ card.deps }}
                </span>
                <span
                  v-if="card.manual"
                  class="manual mono"
                  title="auto=0：链不会自动推进它，要人派发"
                >
                  手动
                </span>
              </span>
            </div>
            <div class="card-meta mono">
              <span class="assignee" :class="{ 'assignee--none': !card.todo.assignee }">
                {{ card.todo.assignee || '未指派' }}
              </span>
              <template v-if="card.job">
                <StatusBadge :status="card.job.status" />
                <button
                  class="chip mono"
                  type="button"
                  :title="`打开 ${card.job.id}`"
                  @click="openJob(card.job.id)"
                >
                  {{ shortId(card.job.id) }} &rarr;
                </button>
                <span
                  v-if="card.job.verify"
                  class="verify mono"
                  :class="card.job.verifyCls"
                  :title="`验收步骤：${card.job.verify}`"
                >
                  {{ card.job.verify }}
                </span>
              </template>
            </div>
            <div v-if="card.job && card.job.usage" class="card-usage mono">
              {{ card.job.usage }}
            </div>
            <p v-if="card.todo.dispatch_error" class="card-error mono">
              派发失败：{{ card.todo.dispatch_error }}
            </p>
            <RouterLink v-if="c.key === 'needs_review'" class="card-link mono" to="/review">
              去验收 &rarr;
            </RouterLink>
          </article>
          <div v-if="c.cards.length === 0" class="col-empty mono">—</div>
        </div>
      </section>
    </div>

    <!-- 没 assignee 的条目拖到 ready：先选 agent，再和 status=ready 一起 PATCH。 -->
    <div v-if="pickerTodo" class="modal-backdrop" @click.self="pickerTodo = null">
      <div class="modal mono">
        <p class="modal-title">派发「{{ pickerTodo.title }}」——先选 agent</p>
        <select v-if="agents.length > 0" v-model="pickAgent" class="modal-input mono">
          <option v-for="a in agents" :key="a.key" :value="a.key">{{ a.key }} · {{ a.type }}</option>
        </select>
        <input
          v-else
          v-model="pickAgent"
          class="modal-input mono"
          placeholder="agent key（如 omp）"
        />
        <div class="modal-actions">
          <button
            class="op-btn mono"
            type="button"
            :disabled="!pickAgent.trim()"
            @click="confirmPick"
          >
            确认派发
          </button>
          <button class="op-btn mono" type="button" @click="pickerTodo = null">取消</button>
        </div>
      </div>
    </div>

    <!-- 人工标记 done/skipped 是终态操作：先确认再 PATCH。 -->
    <div v-if="confirmTarget" class="modal-backdrop" @click.self="confirmTarget = null">
      <div class="modal mono">
        <p class="modal-title">
          把「{{ confirmTarget.todo.title }}」标记为 {{ confirmTarget.status }}？
        </p>
        <div class="modal-actions">
          <button class="op-btn mono" type="button" @click="confirmMark">确认</button>
          <button class="op-btn mono" type="button" @click="confirmTarget = null">取消</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ≤940px 横向滚动：列有 220px 最小宽，容器放不下就滚（不压缩卡片）。 */
.board {
  display: grid;
  grid-template-columns: repeat(5, minmax(220px, 1fr));
  gap: 10px;
  overflow-x: auto;
  padding-bottom: 4px;
}
.col {
  display: flex;
  flex-direction: column;
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
}
/* 拖动时不允许落下的列（doing/needs_review，或规则不允许的转移）：压暗 + 禁放光标。 */
.col--disabled {
  opacity: 0.5;
}
.col--over {
  border-color: var(--phosphor);
  box-shadow: inset 0 0 0 1px var(--phosphor);
}
.col-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-bottom: 1px solid var(--line);
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.06em;
}
.col-count {
  color: var(--paper);
}
.col-body {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 8px;
  padding: 8px;
  min-height: 60px;
}
.col-empty {
  color: var(--queue);
  font-size: 12px;
  text-align: center;
  padding: 6px 0;
}
.zones {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
}
.zone {
  border: 1px dashed var(--line);
  border-radius: var(--radius);
  padding: 8px 0;
  text-align: center;
  color: var(--queue);
  font-size: 11px;
}
.zone--over {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  cursor: grab;
}
.card--dragging {
  opacity: 0.45;
}
.card--busy {
  cursor: progress;
}
/* skipped 归 done 列尾：压暗表示"没干活但算了结"。 */
.card--skipped {
  opacity: 0.6;
}
.card-top {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
.card-title {
  color: var(--paper);
  font-size: 13px;
  word-break: break-word;
  flex: 1;
  min-width: 0;
}
.card-flags {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: none;
}
.dep {
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 5px;
  font-size: 11px;
}
/* 依赖全部满足 = 绿（这一项到点了）；否则灰（还在等）。 */
.dep--ok {
  color: var(--done);
  border-color: var(--done);
}
.manual {
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 5px;
  font-size: 11px;
}
.card-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 11px;
}
.assignee {
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 6px;
}
.assignee--none {
  color: var(--queue);
}
.chip {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 6px;
  font-size: 11px;
}
.chip:hover {
  border-color: var(--phosphor);
}
.verify {
  font-size: 11px;
}
.verify--ok {
  color: var(--done);
}
.verify--bad {
  color: var(--fail);
}
.verify--skip {
  color: var(--queue);
}
.card-usage {
  color: var(--paper);
  font-size: 11px;
}
.card-error {
  margin: 0;
  color: var(--fail);
  font-size: 11px;
  word-break: break-word;
}
.card-link {
  color: var(--phosphor);
  font-size: 11px;
}
.modal-backdrop {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.45);
}
.modal {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 320px;
  max-width: 90vw;
  padding: 14px;
  background: var(--panel);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
}
.modal-title {
  margin: 0;
  color: var(--paper);
  font-size: 13px;
  word-break: break-word;
}
.modal-input {
  background: var(--term-bg);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 6px 8px;
  font-size: 12px;
  outline: none;
}
.modal-input:focus {
  border-color: var(--phosphor);
}
.modal-actions {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.op-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.op-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.op-btn:disabled {
  opacity: 0.55;
  cursor: default;
}
</style>
