<script setup lang="ts">
// 收集箱条目的分派对话框：推荐的前 3 个会话（带命中原因，推断的要标出来）+ 会话选择器；
// 动作：交给会话 / 新建 job / 转 todo / 转 issue / 归档 / 丢弃。失败时 409 显示服务端的 detail，
// 条目仍留在收集箱。新建 job 先去 NewJob 页提交，提交后那里再关联（见 utils/dispatch.ts）。
import { computed, onMounted, reactive, ref } from 'vue'
import { ApiError, dispatchWorkItem, getDispatchTargets, listPlans, listTrackerRepos } from '../api/client'
import { useWhoami } from '../store/whoami'
import {
  buildDispatchRequest,
  canManagePlan,
  canReplySession,
  DISPATCH_ACTIONS,
  dispatchErrorText,
  dispatchVisibility,
  EMPTY_DISPATCH_FORM,
  issueRepoLabel,
  issueRepos,
  newJobLink,
  PENDING_SYNC_HINT,
  PENDING_SYNC_LABEL,
  targetRows,
} from '../utils/dispatch'
import type { DispatchTo } from '../utils/dispatch'
import type { AgentSession, Plan, TrackerRepo, WorkDetail } from '../api/types'

const props = defineProps<{
  item: WorkDetail
  // 会话全量列表：会话选择器的候选
  sessions: AgentSession[]
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'done', detail: WorkDetail, text: string): void
  (e: 'refresh'): void
}>()

const { whoami } = useWhoami()
const visible = computed(() => dispatchVisibility(whoami.value))
const actions = computed(() => DISPATCH_ACTIONS.filter((a) => visible.value[a.to]))

const to = ref<DispatchTo>('session')
const form = reactive({ ...EMPTY_DISPATCH_FORM, issueTitle: props.item.title, newPlanTitle: props.item.title })
const busy = ref(false)
const error = ref('')
const targetsLoading = ref(true)
const targets = ref<ReturnType<typeof targetRows>>([])
const plans = ref<Plan[]>([])
const repos = ref<TrackerRepo[]>([])

const usableRepos = computed(() => issueRepos(repos.value))
const hiddenRepos = computed(() => repos.value.length - usableRepos.value.length)
const rec = computed(() => targets.value.filter((t) => canReplySession(whoami.value, { caller_id: t.callerId })))
const recHidden = computed(() => targets.value.length - rec.value.length)
const pickable = computed(() =>
  props.sessions.filter((s) => s.state !== 'ended' && canReplySession(whoami.value, s)),
)
const planOptions = computed(() => plans.value.filter((p) => canManagePlan(whoami.value, p.owner)))

onMounted(() => {
  void getDispatchTargets(props.item.id)
    .then((r) => (targets.value = targetRows(r.targets)))
    .catch(() => (targets.value = []))
    .finally(() => (targetsLoading.value = false))
  void listPlans({ statuses: ['open', 'blocked'], limit: 100 }).then((r) => (plans.value = r.plans ?? [])).catch(() => {})
  void listTrackerRepos().then((r) => {
    repos.value = r.repos ?? []
    if (!form.trackerId && usableRepos.value.length === 1) form.trackerId = usableRepos.value[0].tracker_id
  }).catch(() => {})
})

function choose(t: DispatchTo): void {
  to.value = t
  error.value = ''
}

const submitLabel = computed(() => ({
  session: '交给会话', job: '去新建 job', todo: '转 todo', issue: '转 issue', archive: '归档', drop: '丢弃',
}[to.value]))

const doneText: Record<DispatchTo, string> = {
  session: '已交给会话，条目转为进行中',
  job: '已关联 job',
  todo: '已转为 todo',
  issue: '已转为 issue，同步前显示「' + PENDING_SYNC_LABEL + '」',
  archive: '已归档',
  drop: '已丢弃',
}

async function submit(): Promise<void> {
  if (busy.value || to.value === 'job') return
  if (to.value === 'drop' && !window.confirm('丢弃这条？它会标为已放弃。')) return
  const built = buildDispatchRequest(to.value, form)
  if (!built.req) {
    error.value = built.error
    return
  }
  busy.value = true
  error.value = ''
  try {
    const d = await dispatchWorkItem(props.item.id, built.req)
    emit('done', d, doneText[to.value])
  } catch (e) {
    const v = dispatchErrorText(to.value, e instanceof ApiError ? e : { message: e instanceof Error ? e.message : String(e) })
    error.value = v.text
    if (v.refresh) emit('refresh')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="dd-mask" @click.self="emit('close')">
    <div class="dd" role="dialog" aria-label="分派收集箱条目" data-test="dispatch-dlg" @keydown.esc="emit('close')">
      <header class="dd-head">
        <h4 class="dd-title mono">分派</h4>
        <p class="dd-item" :title="item.title">{{ item.title }}</p>
      </header>

      <div class="dd-acts mono" role="radiogroup" aria-label="分派方式">
        <button
          v-for="a in actions"
          :key="a.to"
          type="button"
          role="radio"
          class="dd-act"
          :class="{ on: to === a.to }"
          :aria-checked="to === a.to"
          :title="a.hint"
          :data-test="`dispatch-act-${a.to}`"
          @click="choose(a.to)"
        >{{ a.label }}</button>
      </div>

      <section v-if="to === 'session'" class="dd-body" data-test="dispatch-session">
        <p v-if="targetsLoading" class="dd-hint mono">正在找合适的会话…</p>
        <template v-else>
          <p v-if="rec.length" class="dd-hint mono">按标题和目标匹配会话主题、范围、会话名和项目，推荐 {{ rec.length }} 个：</p>
          <p v-else class="dd-hint mono" data-test="dispatch-no-rec">没有找到明显匹配的会话，从下面选一个。</p>
          <ul v-if="rec.length" class="dd-rec">
            <li v-for="t in rec" :key="t.sessionId">
              <label class="dd-card" :class="{ on: form.sessionId === t.sessionId }" data-test="dispatch-target">
                <input v-model="form.sessionId" type="radio" name="dd-sid" :value="t.sessionId" />
                <span class="dd-card-main">
                  <span class="dd-card-title">{{ t.title }}<span class="dd-agent mono">{{ t.agent }}</span><span v-if="t.project" class="dd-agent mono">{{ t.project }}</span></span>
                  <span v-if="t.topic" class="dd-topic">{{ t.topic }}<em v-if="t.topicInferred" class="dd-inf mono" data-test="dispatch-topic-inferred">推断</em></span>
                  <span class="dd-hits">
                    <span v-for="(h, i) in t.hits" :key="i" class="dd-hit mono" :class="{ 'dd-hit--inf': h.inferred }" data-test="dispatch-hit">{{ h.text }}</span>
                    <span v-if="t.maybeOffline" class="dd-hit dd-hit--off mono" data-test="dispatch-offline">{{ t.offlineLabel }}</span>
                  </span>
                </span>
              </label>
            </li>
          </ul>
          <p v-if="recHidden" class="dd-hint mono">另有 {{ recHidden }} 个他人的会话，你没有向其传话的权限，已隐藏。</p>
        </template>
        <label class="dd-field mono">
          <span>或选择其他会话</span>
          <select v-model="form.sessionId" class="dd-in mono" data-test="dispatch-session-pick">
            <option value="">选一个会话…</option>
            <option v-for="s in pickable" :key="s.session_id" :value="s.session_id">{{ s.title || s.peer_name || s.session_id.slice(0, 8) }} · {{ s.agent }}<template v-if="s.project_key"> · {{ s.project_key }}</template></option>
          </select>
        </label>
      </section>

      <section v-else-if="to === 'job'" class="dd-body" data-test="dispatch-job">
        <p class="dd-hint mono">打开新建 job 页：标题、目标、项目已预填。提交后 job 自动关联到这条，条目转为进行中。</p>
      </section>

      <section v-else-if="to === 'todo'" class="dd-body" data-test="dispatch-todo">
        <label class="dd-field mono">
          <span>加到哪个 plan</span>
          <select v-model="form.planId" class="dd-in mono" data-test="dispatch-plan">
            <option value="">＋ 新建 plan</option>
            <option v-for="p in planOptions" :key="p.plan_id" :value="p.plan_id">{{ p.title || p.plan_id }}</option>
          </select>
        </label>
        <label v-if="!form.planId" class="dd-field mono">
          <span>新 plan 标题</span>
          <input v-model="form.newPlanTitle" class="dd-in mono" type="text" data-test="dispatch-plan-title" />
        </label>
      </section>

      <section v-else-if="to === 'issue'" class="dd-body" data-test="dispatch-issue">
        <label class="dd-field mono">
          <span>仓库</span>
          <select v-model="form.trackerId" class="dd-in mono" data-test="dispatch-repo">
            <option value="">选一个仓库…</option>
            <option v-for="r in usableRepos" :key="r.tracker_id" :value="r.tracker_id">{{ issueRepoLabel(r) }}</option>
          </select>
        </label>
        <p v-if="!usableRepos.length" class="dd-hint mono" data-test="dispatch-no-repo">没有可用的仓库。仓库要先在本地执行一次 gofer repo sync，server 才知道它的 id 前缀。</p>
        <p v-else-if="hiddenRepos" class="dd-hint mono">{{ hiddenRepos }} 个仓库还没有 id 前缀，未列出。</p>
        <label class="dd-field mono">
          <span>标题（默认用条目标题）</span>
          <input v-model="form.issueTitle" class="dd-in mono" type="text" data-test="dispatch-issue-title" />
        </label>
        <p class="dd-hint mono">{{ PENDING_SYNC_HINT }}。</p>
      </section>

      <section v-else-if="to === 'archive'" class="dd-body">
        <p class="dd-hint mono">不再处理，标为已完成，日志记「归档」。</p>
      </section>
      <section v-else class="dd-body">
        <p class="dd-hint mono">放弃这条，标为已放弃，日志记「丢弃」。</p>
      </section>

      <p v-if="error" class="dd-err mono" role="alert" data-test="dispatch-error">{{ error }}</p>
      <div class="dd-actions">
        <RouterLink v-if="to === 'job'" class="dd-submit mono" :to="newJobLink(item.id)" data-test="dispatch-new-job" @click="emit('close')">{{ submitLabel }}</RouterLink>
        <button v-else class="dd-submit mono" type="button" :disabled="busy" data-test="dispatch-submit" @click="submit">{{ busy ? '处理中…' : submitLabel }}</button>
        <button class="dd-cancel mono" type="button" :disabled="busy" @click="emit('close')">取消</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dd-mask { position: fixed; inset: 0; z-index: 90; background: rgba(0, 0, 0, 0.5); display: flex; align-items: center; justify-content: center; padding: 16px; }
.dd { width: min(520px, 100%); max-height: 100%; overflow: auto; display: flex; flex-direction: column; gap: 12px; padding: 14px; background: var(--panel); border: 1px solid var(--line); border-top: 2px solid var(--phosphor); border-radius: var(--radius); box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5); }
.dd-head { display: flex; flex-direction: column; gap: 4px; }
.dd-title { margin: 0; font-size: 12px; color: var(--phosphor); }
.dd-item { margin: 0; font-size: 13px; line-height: 1.5; color: var(--paper); overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.dd-acts { display: flex; flex-wrap: wrap; border: 1px solid var(--line); border-radius: var(--radius); overflow: hidden; }
.dd-act { flex: 1 1 auto; padding: 7px 8px; font-size: 12px; color: var(--queue); background: transparent; border: 0; border-right: 1px solid var(--line); cursor: pointer; white-space: nowrap; }
.dd-act:last-child { border-right: 0; }
.dd-act.on { color: var(--paper); background: color-mix(in srgb, var(--phosphor) 18%, transparent); }
.dd-act:focus-visible, .dd-submit:focus-visible { outline: 2px solid var(--phosphor); outline-offset: -2px; }
.dd-body { display: flex; flex-direction: column; gap: 10px; }
.dd-hint { margin: 0; font-size: 11px; line-height: 1.5; color: var(--queue); }
.dd-rec { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.dd-card { display: flex; gap: 8px; align-items: flex-start; padding: 8px 10px; border: 1px solid var(--line); border-left: 3px solid var(--queue); border-radius: var(--radius); cursor: pointer; }
.dd-card.on, .dd-card:hover { border-color: var(--phosphor); }
.dd-card input { margin-top: 3px; }
.dd-card-main { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.dd-card-title { font-size: 13px; color: var(--paper); overflow-wrap: anywhere; }
.dd-agent { margin-left: 8px; font-size: 11px; color: var(--queue); }
.dd-topic { font-size: 12px; color: var(--paper); opacity: 0.85; overflow-wrap: anywhere; }
.dd-inf { margin-left: 6px; font-style: normal; font-size: 10px; padding: 0 5px; color: var(--run); border: 1px dashed var(--run); border-radius: 8px; }
.dd-hits { display: flex; flex-wrap: wrap; gap: 4px; }
.dd-hit { font-size: 11px; padding: 0 7px; color: var(--phosphor); border: 1px solid var(--line); border-radius: 9px; overflow-wrap: anywhere; }
.dd-hit--inf { color: var(--run); border-style: dashed; border-color: var(--run); }
.dd-hit--off { color: var(--queue); }
.dd-field { display: flex; flex-direction: column; gap: 4px; font-size: 11px; color: var(--queue); }
.dd-in { width: 100%; box-sizing: border-box; padding: 6px 8px; color: var(--paper); background: var(--term-bg); border: 1px solid var(--line); border-radius: var(--radius); font: inherit; font-size: 13px; }
.dd-in:focus { outline: none; border-color: var(--phosphor); }
.dd-err { margin: 0; padding: 6px 10px; font-size: 12px; color: var(--fail); border: 1px solid var(--fail); border-radius: var(--radius); overflow-wrap: anywhere; }
.dd-actions { display: flex; gap: 8px; }
.dd-submit, .dd-cancel { padding: 6px 16px; font-size: 12px; color: var(--paper); background: transparent; border: 1px solid var(--line); border-radius: var(--radius); cursor: pointer; text-decoration: none; }
.dd-submit { border-color: var(--phosphor); color: var(--phosphor); font-weight: 600; }
.dd-submit:disabled, .dd-cancel:disabled { opacity: 0.5; cursor: not-allowed; }
@media (max-width: 768px) {
  .dd-mask { align-items: flex-end; padding: 0; }
  .dd { width: 100%; border-radius: var(--radius) var(--radius) 0 0; padding-bottom: calc(14px + env(safe-area-inset-bottom, 0px)); }
  .dd-act { padding: 10px 8px; font-size: 13px; }
  .dd-in { font-size: 16px; }
  .dd-submit, .dd-cancel { padding: 10px 18px; font-size: 14px; }
}
</style>
