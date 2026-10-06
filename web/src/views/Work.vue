<script setup lang="ts">
// 「工作」页：全局总览。一张卡片 = 一件事（跨会话），按状态分栏或按工作区分组，
// 顶部「等我」「到期提醒」「未整理」三个计数徽标可直接筛选。手机优先（单列）。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { createLiveTopic } from '../utils/useLiveTopic'
import {
  acceptWorkSuggestion,
  ApiError,
  createWorkItem,
  dismissWorkSuggestion,
  listAgentSessions,
  listWorkItems,
  patchWorkItem,
  resumeSession,
  summarizeWorkItem,
} from '../api/client'
import SessionDrawer from '../components/SessionDrawer.vue'
import WorkCard from '../components/WorkCard.vue'
import WorkDrawer from '../components/WorkDrawer.vue'
import { pruneExpanded, toggleExpanded } from '../utils/cardExpand'
import { resumeConfirmText, resumeFailText } from '../utils/sessionResume'
import {
  groupByStatus,
  groupByWorkspace,
  sortCards,
  unsortedItems,
  visibleItems,
  type WorkFilter,
} from '../utils/work'
import type { AgentSession, WorkItem, WorkStatus } from '../api/types'

const route = useRoute()
const router = useRouter()

const items = ref<WorkItem[]>([])
const sessions = ref<AgentSession[]>([])
const loading = ref(false)
const error = ref('')
const actionError = ref('')
const busyIds = ref<Set<string>>(new Set())
const nowSec = ref(Math.floor(Date.now() / 1000))

const VIEW_KEY = 'gofer.work.view'
function readView(): 'status' | 'workspace' {
  try {
    return localStorage.getItem(VIEW_KEY) === 'workspace' ? 'workspace' : 'status'
  } catch {
    return 'status'
  }
}
const view = ref<'status' | 'workspace'>(readView())
watch(view, (v) => {
  try {
    localStorage.setItem(VIEW_KEY, v)
  } catch {
    // 记不住视图偏好不影响使用
  }
})

const filter = ref<WorkFilter>('all')
const q = ref('')
const showClosed = ref(false)
const expanded = ref<Set<string>>(new Set())

const openId = ref(typeof route.query.id === 'string' ? route.query.id : '')
const openSid = ref('')

const sessionMap = computed<Record<string, AgentSession>>(() => Object.fromEntries(sessions.value.map((s) => [s.session_id, s])))

// ---------------- 数据 ----------------
async function load(opts?: { silent?: boolean }): Promise<void> {
  if (!opts?.silent) loading.value = true
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const [w, s] = await Promise.all([
      listWorkItems({ closed: showClosed.value, limit: 1000 }),
      listAgentSessions({ all: true, limit: 300 }).catch(() => ({ sessions: [] as AgentSession[] })),
    ])
    items.value = w.items ?? []
    sessions.value = s.sessions ?? []
    expanded.value = pruneExpanded(expanded.value, items.value.map((i) => i.id))
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const liveWork = createLiveTopic('work', { initial: false, fetch: () => load({ silent: true }) })
// 会话状态 / 名称变化也影响卡片上的会话信息
const liveSessions = createLiveTopic('sessions', { initial: false, fetch: () => load({ silent: true }) })

watch(showClosed, () => void load())

// ---------------- 视图 ----------------
const open = computed(() => items.value.filter((i) => i.status !== 'done' && i.status !== 'dropped'))
const needsMeCount = computed(() => open.value.filter((i) => i.status === 'needs_me').length)
const dueCount = computed(() => open.value.filter((i) => i.due).length)
const unsorted = computed(() => sortCards(unsortedItems(items.value)))
const unsortedCount = computed(() => unsorted.value.length)

const shown = computed(() => visibleItems(items.value, { filter: filter.value, q: q.value, showClosed: showClosed.value }))
const filtering = computed(() => filter.value !== 'all')
const columns = computed(() =>
  groupByStatus(shown.value, { showClosed: showClosed.value, includeUnsorted: filtering.value || !!q.value.trim() }),
)
const groups = computed(() => groupByWorkspace(shown.value))
// 未整理区只在不筛选、不搜索时单独列出（筛选时它们直接参与结果）
const showUnsortedArea = computed(() => !filtering.value && !q.value.trim() && unsorted.value.length > 0)

function setFilter(f: WorkFilter): void {
  filter.value = filter.value === f ? 'all' : f
}

function toggleCard(id: string): void {
  expanded.value = toggleExpanded(expanded.value, id)
}

// ---------------- 抽屉 ----------------
function openItem(id: string): void {
  openId.value = id
  if (route.query.id !== id) void router.replace({ query: { ...route.query, id } })
}
function closeItem(): void {
  openId.value = ''
  if (route.query.id) {
    const rest = { ...route.query }
    delete rest.id
    void router.replace({ query: rest })
  }
}
watch(
  () => route.query.id,
  (v) => {
    openId.value = typeof v === 'string' ? v : ''
  },
)

function openSession(sid: string): void {
  openSid.value = sid
}

// ---------------- 操作 ----------------
function setBusy(id: string, on: boolean): void {
  const next = new Set(busyIds.value)
  if (on) next.add(id)
  else next.delete(id)
  busyIds.value = next
}

async function setStatus(it: WorkItem, status: WorkStatus): Promise<void> {
  if (busyIds.value.has(it.id)) return
  if (status === 'dropped' && !window.confirm(`放弃「${it.title}」？`)) return
  setBusy(it.id, true)
  actionError.value = ''
  try {
    await patchWorkItem(it.id, { status, rev: it.rev })
    await load({ silent: true })
  } catch (e) {
    actionError.value =
      e instanceof ApiError && e.status === 409 ? `「${it.title}」刚被改过，已刷新，请再试一次。` : e instanceof Error ? e.message : String(e)
    await load({ silent: true })
  } finally {
    setBusy(it.id, false)
  }
}

// 整理：后台一次性只读 job；结果经 work 推送回来（卡片上出现整理建议 / 字段被填上）。
async function summarize(it: WorkItem): Promise<void> {
  if (busyIds.value.has(it.id)) return
  setBusy(it.id, true)
  actionError.value = ''
  try {
    await summarizeWorkItem(it.id)
    await load({ silent: true })
  } catch (e) {
    actionError.value = e instanceof ApiError ? (e.detail ? `${e.message}：${e.detail}` : e.message) : e instanceof Error ? e.message : String(e)
  } finally {
    setBusy(it.id, false)
  }
}

async function suggestion(it: WorkItem, field: string, accept: boolean): Promise<void> {
  if (busyIds.value.has(it.id)) return
  setBusy(it.id, true)
  actionError.value = ''
  try {
    await (accept ? acceptWorkSuggestion(it.id, field) : dismissWorkSuggestion(it.id, field))
    await load({ silent: true })
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
    await load({ silent: true })
  } finally {
    setBusy(it.id, false)
  }
}

async function wake(sid: string): Promise<void> {
  const s = sessionMap.value[sid]
  if (!s?.can_resume || busyIds.value.has(sid)) return
  if (!window.confirm(resumeConfirmText(s))) return
  setBusy(sid, true)
  actionError.value = ''
  try {
    const res = await resumeSession(sid)
    if (res.job_id) await router.push(`/jobs/${encodeURIComponent(res.job_id)}?attach=1`)
    else await load({ silent: true })
  } catch (e) {
    actionError.value = `唤醒失败：${resumeFailText(e)}`
  } finally {
    setBusy(sid, false)
  }
}

// 新建
const createOpen = ref(false)
const newTitle = ref('')
const newGoal = ref('')
const creating = ref(false)
async function create(): Promise<void> {
  const title = newTitle.value.trim()
  if (!title || creating.value) return
  creating.value = true
  actionError.value = ''
  try {
    const d = await createWorkItem({ title, goal: newGoal.value.trim() || undefined })
    newTitle.value = ''
    newGoal.value = ''
    createOpen.value = false
    await load({ silent: true })
    openItem(d.id)
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

onMounted(() => {
  void load()
  liveWork.start()
  liveSessions.start()
})
onUnmounted(() => {
  liveWork.stop()
  liveSessions.stop()
})
</script>

<template>
  <div class="work-page">
    <header class="work-head">
      <h1 class="title mono">工作</h1>
      <div class="work-controls mono">
        <div class="seg" role="group" aria-label="分组方式">
          <button type="button" class="seg-btn" :class="{ on: view === 'status' }" data-test="view-status" @click="view = 'status'">按状态</button>
          <button type="button" class="seg-btn" :class="{ on: view === 'workspace' }" data-test="view-workspace" @click="view = 'workspace'">按工作区</button>
        </div>
        <label class="check"><input v-model="showClosed" type="checkbox" /> 显示已完成</label>
        <button class="icard-btn mono" type="button" :disabled="loading" @click="load()">{{ loading ? '刷新中…' : '刷新' }}</button>
        <button class="icard-btn icard-btn--primary mono" type="button" data-test="new-toggle" @click="createOpen = !createOpen">{{ createOpen ? '收起' : '＋ 新建' }}</button>
      </div>
    </header>

    <div class="work-chips mono" data-test="work-chips">
      <button type="button" class="count-chip" :class="{ on: filter === 'needs_me', hot: needsMeCount > 0 }" data-test="chip-needs-me" @click="setFilter('needs_me')">
        等我 <strong>{{ needsMeCount }}</strong>
      </button>
      <button type="button" class="count-chip" :class="{ on: filter === 'due', hot: dueCount > 0 }" data-test="chip-due" @click="setFilter('due')">
        到期提醒 <strong>{{ dueCount }}</strong>
      </button>
      <button type="button" class="count-chip" :class="{ on: filter === 'unsorted' }" data-test="chip-unsorted" @click="setFilter('unsorted')">
        未整理 <strong>{{ unsortedCount }}</strong>
      </button>
      <input v-model="q" class="work-search mono" type="search" placeholder="搜索标题 / 目标 / 阻塞…" data-test="search" />
      <button v-if="filter !== 'all'" type="button" class="link-btn mono" @click="filter = 'all'">清除筛选</button>
    </div>

    <section v-if="createOpen" class="work-create" data-test="create-form">
      <input v-model="newTitle" class="work-search mono" type="text" placeholder="这件事是什么？" data-test="new-title" @keydown.enter="create" />
      <input v-model="newGoal" class="work-search mono" type="text" placeholder="目标（可选）" />
      <button class="icard-btn icard-btn--primary mono" type="button" :disabled="creating || !newTitle.trim()" data-test="new-submit" @click="create">创建</button>
    </section>

    <p v-if="error" class="error mono">{{ error }}</p>
    <p v-if="actionError" class="error mono" data-test="action-error">{{ actionError }}</p>

    <!-- 未整理：自动生成、还没补目标的草稿，不打扰人，只在这里集中 -->
    <section v-if="showUnsortedArea" class="work-group" data-test="unsorted-area">
      <h2 class="group-title mono">未整理 <span class="group-count mono">{{ unsorted.length }}</span></h2>
      <p class="group-hint mono">会话第一次提问时自动生成的草稿。补上目标（或让会话汇报）后会进入看板；也可以合并到已有的工作项。</p>
      <div class="icard-grid">
        <WorkCard
          v-for="it in unsorted"
          :key="it.id"
          :item="it"
          :expanded="expanded.has(it.id)"
          :active="openId === it.id"
          :sessions="sessionMap"
          :busy="busyIds.has(it.id)"
          :now-sec="nowSec"
          @toggle="toggleCard(it.id)"
          @open="openItem(it.id)"
          @open-session="openSession"
          @wake="wake"
          @set-status="(st) => setStatus(it, st)"
          @summarize="summarize(it)"
          @accept-suggestion="(f) => suggestion(it, f, true)"
          @dismiss-suggestion="(f) => suggestion(it, f, false)"
        />
      </div>
    </section>

    <!-- 筛选 / 搜索：平铺结果 -->
    <section v-if="filtering" class="work-group" data-test="filtered-area">
      <h2 class="group-title mono">筛选结果 <span class="group-count mono">{{ shown.length }}</span></h2>
      <div v-if="shown.length" class="icard-grid">
        <WorkCard
          v-for="it in sortCards(shown)"
          :key="it.id"
          :item="it"
          :expanded="expanded.has(it.id)"
          :active="openId === it.id"
          :sessions="sessionMap"
          :busy="busyIds.has(it.id)"
          :now-sec="nowSec"
          @toggle="toggleCard(it.id)"
          @open="openItem(it.id)"
          @open-session="openSession"
          @wake="wake"
          @set-status="(st) => setStatus(it, st)"
          @summarize="summarize(it)"
          @accept-suggestion="(f) => suggestion(it, f, true)"
          @dismiss-suggestion="(f) => suggestion(it, f, false)"
        />
      </div>
      <div v-else class="empty mono">没有符合条件的工作项</div>
    </section>

    <!-- 按状态分栏 -->
    <div v-else-if="view === 'status'" class="work-cols" data-test="status-view">
      <section v-for="col in columns" :key="col.status" class="work-col" :class="{ 'work-col--empty': !col.items.length }" :data-status="col.status">
        <h2 class="col-title mono">
          <span class="sbadge" :class="`sbadge--${col.tone}`">{{ col.label }}</span>
          <span class="group-count mono">{{ col.items.length }}</span>
        </h2>
        <div class="col-cards">
          <WorkCard
            v-for="it in col.items"
            :key="it.id"
            :item="it"
            :expanded="expanded.has(it.id)"
            :active="openId === it.id"
            :sessions="sessionMap"
            :busy="busyIds.has(it.id)"
            :now-sec="nowSec"
            @toggle="toggleCard(it.id)"
            @open="openItem(it.id)"
            @open-session="openSession"
            @wake="wake"
            @set-status="(st) => setStatus(it, st)"
          @summarize="summarize(it)"
          @accept-suggestion="(f) => suggestion(it, f, true)"
          @dismiss-suggestion="(f) => suggestion(it, f, false)"
          />
          <p v-if="!col.items.length" class="col-empty mono">—</p>
        </div>
      </section>
    </div>

    <!-- 按工作区分组 -->
    <div v-else class="work-groups" data-test="workspace-view">
      <section v-for="g in groups" :key="g.key" class="work-group">
        <h2 class="group-title mono" :title="g.key">{{ g.label }} <span class="group-count mono">{{ g.items.length }}</span></h2>
        <div class="icard-grid">
          <WorkCard
            v-for="it in g.items"
            :key="it.id"
            :item="it"
            :expanded="expanded.has(it.id)"
            :active="openId === it.id"
            :sessions="sessionMap"
            :busy="busyIds.has(it.id)"
            :now-sec="nowSec"
            @toggle="toggleCard(it.id)"
            @open="openItem(it.id)"
            @open-session="openSession"
            @wake="wake"
            @set-status="(st) => setStatus(it, st)"
          @summarize="summarize(it)"
          @accept-suggestion="(f) => suggestion(it, f, true)"
          @dismiss-suggestion="(f) => suggestion(it, f, false)"
          />
        </div>
      </section>
      <div v-if="!groups.length" class="empty mono">暂无工作项</div>
    </div>

    <div v-if="!loading && !error && items.length === 0" class="empty mono" data-test="empty">
      还没有工作项。会话第一次有人提问时会自动生成草稿；也可以点右上角「＋ 新建」。
    </div>

    <WorkDrawer
      v-if="openId"
      :id="openId"
      :sessions="sessions"
      :items="items"
      @close="closeItem"
      @changed="load({ silent: true })"
      @open-session="openSession"
      @wake="wake"
      @open-item="openItem"
    />
    <SessionDrawer v-if="openSid" :sid="openSid" @close="openSid = ''" @changed="load({ silent: true })" @deleted="openSid = ''" />
  </div>
</template>

<style scoped>
.work-page { max-width: 1480px; margin: 0 auto; }
.work-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 10px; }
.title { margin: 0; font-size: 16px; letter-spacing: 0.08em; color: var(--paper); }
.work-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; font-size: 12px; color: var(--queue); }
.seg { display: inline-flex; border: 1px solid var(--line); border-radius: var(--radius); overflow: hidden; }
.seg-btn { padding: 4px 10px; font-size: 11px; color: var(--queue); background: transparent; border: 0; border-right: 1px solid var(--line); cursor: pointer; }
.seg-btn:last-child { border-right: 0; }
.seg-btn.on { color: var(--paper); background: rgba(79, 176, 198, 0.18); }
.check { display: inline-flex; align-items: center; gap: 5px; font-size: 11px; color: var(--queue); cursor: pointer; }
.check input { accent-color: var(--phosphor); margin: 0; }
.work-chips { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 12px; }
.count-chip { padding: 4px 12px; font-size: 12px; color: var(--paper); background: var(--panel); border: 1px solid var(--line); border-radius: 14px; cursor: pointer; }
.count-chip strong { margin-left: 4px; }
.count-chip.hot { border-color: var(--run); color: var(--run); }
.count-chip.on { border-color: var(--phosphor); box-shadow: 0 0 0 1px var(--phosphor) inset; }
.work-search { flex: 1 1 200px; min-width: 0; color: var(--paper); background: var(--ink); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 8px; font-size: 12px; }
.work-create { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; padding: 10px; background: var(--panel); border: 1px solid var(--line); border-radius: var(--radius); }
.link-btn { background: none; border: 0; padding: 0 4px; font-size: 11px; color: var(--phosphor); cursor: pointer; text-decoration: underline; }
.error { color: var(--fail); font-size: 12px; border: 1px solid var(--fail); border-radius: var(--radius); padding: 8px 10px; margin: 0 0 12px; word-break: break-word; }
.group-title { display: flex; align-items: center; gap: 8px; margin: 0 0 8px; font-size: 13px; letter-spacing: 0.06em; color: var(--paper); }
.group-count { border: 1px solid var(--line); border-radius: 9px; padding: 0 7px; font-size: 10px; color: var(--queue); }
.group-hint { margin: -4px 0 8px; font-size: 11px; color: var(--queue); }
.work-group { margin-bottom: 22px; }
.work-cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 14px; align-items: start; }
.work-col { min-width: 0; }
.col-title { display: flex; align-items: center; gap: 8px; margin: 0 0 8px; font-size: 12px; }
.col-cards { display: flex; flex-direction: column; gap: 10px; }
.col-empty { margin: 0; padding: 14px 0; text-align: center; color: var(--queue); font-size: 12px; border: 1px dashed var(--line); border-radius: var(--radius); }
.empty { border: 1px solid var(--line); border-radius: var(--radius); color: var(--queue); font-size: 13px; padding: 28px 14px; text-align: center; }
@media (max-width: 640px) {
  .work-cols { grid-template-columns: minmax(0, 1fr); gap: 4px; }
  /* 手机：空的列不占位，免得滑很久才到有内容的列 */
  .work-col--empty { display: none; }
}
</style>
