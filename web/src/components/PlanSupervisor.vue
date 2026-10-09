<script setup lang="ts">
// PLAN-06：plan 绑定的主 agent 会话（supervisor_session_id）。
//  - 显示会话名 / 状态 / 最后心跳，可打开会话抽屉；
//  - 修改（从会话列表选）/ 清除绑定，走 PATCH /v1/plans/{id}（仅 plan owner）；
//  - 「发给主 agent」：复用会话抽屉同一个入口 POST /v1/sessions/{sid}/messages
//    （服务端按 中继 → 传话 → 送话 阶梯投递），附快捷语。
import { computed, onMounted, ref, watch } from 'vue'
import SessionDrawer from './SessionDrawer.vue'
import { getAgentSession, listAgentSessions, sendSessionMessage, setPlanSupervisorSession } from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import type { AgentSession } from '../api/types'
import { HANDOFF_QUICK_PHRASE, canMessageSession, messageOutcomeText, sessionLabel } from '../utils/planSupervisor'

const props = defineProps<{ planId: string; sid: string; project?: string }>()
const emit = defineEmits<{ (e: 'changed', sid: string): void; (e: 'session', s: AgentSession | null): void }>()

const session = ref<AgentSession | null>(null)
const loadError = ref('')
const drawerOpen = ref(false)

const editing = ref(false)
const candidates = ref<AgentSession[]>([])
const pick = ref('')
const bindBusy = ref(false)
const bindError = ref('')

const draft = ref('')
const sending = ref(false)
const sendResult = ref('')
const sendFailed = ref(false)

async function loadSession() {
  session.value = null
  loadError.value = ''
  if (!props.sid) return
  try {
    session.value = (await getAgentSession(props.sid, { limit: 1 })).session
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  }
  // 父页（PlanDetail）头部的「主 Agent 会话」行复用这里读到的会话名，不再单独请求。
  emit('session', session.value)
}

async function startEdit() {
  editing.value = true
  bindError.value = ''
  pick.value = props.sid
  try {
    candidates.value = (await listAgentSessions({ project: props.project || undefined, limit: 100 })).sessions ?? []
  } catch (e) {
    bindError.value = e instanceof Error ? e.message : String(e)
  }
}

async function bind(sid: string) {
  bindBusy.value = true
  bindError.value = ''
  try {
    await setPlanSupervisorSession(props.planId, sid)
    editing.value = false
    emit('changed', sid)
  } catch (e) {
    bindError.value = e instanceof Error ? e.message : String(e)
  } finally {
    bindBusy.value = false
  }
}

async function send() {
  const text = draft.value.trim()
  if (!text || !props.sid) return
  sending.value = true
  sendResult.value = ''
  sendFailed.value = false
  try {
    const m = await sendSessionMessage(props.sid, text)
    sendFailed.value = m.status === 'failed'
    sendResult.value = messageOutcomeText(m)
    if (!sendFailed.value) draft.value = ''
  } catch (e) {
    sendFailed.value = true
    sendResult.value = `发送失败：${e instanceof Error ? e.message : String(e)}`
  } finally {
    sending.value = false
  }
}

// A bound session that ended / went offline / was handed off can no longer act as
// the supervisor; offer to rebind to the project's most recently active session.
const GONE_STATES = new Set(['ended', 'offline', 'handed_off'])
const sessionGone = computed(() => !!session.value && GONE_STATES.has(session.value.state))
const rebindHint = ref('')

async function rebindLatest() {
  rebindHint.value = ''
  bindError.value = ''
  try {
    const list = (await listAgentSessions({ project: props.project || undefined, limit: 100 })).sessions ?? []
    const next = list.find((s) => s.session_id !== props.sid && !GONE_STATES.has(s.state))
    if (!next) {
      rebindHint.value = '本项目没有活跃会话，请先在终端开一个会话或手动选择'
      return
    }
    await bind(next.session_id)
  } catch (e) {
    bindError.value = e instanceof Error ? e.message : String(e)
  }
}

const canSend = computed(() => !!draft.value.trim() && !sending.value && (session.value === null || canMessageSession(session.value)))

onMounted(loadSession)
watch(() => props.sid, loadSession)
</script>

<template>
  <section class="section plan-supervisor">
    <div class="section-head">
      <h2 class="section-title mono">主 agent 会话</h2>
      <div class="ps-actions">
        <button class="op-btn ps-change" type="button" @click="startEdit">{{ sid ? '修改' : '绑定' }}</button>
        <button v-if="sid" class="op-btn ps-clear" type="button" :disabled="bindBusy" @click="bind('')">清除</button>
      </div>
    </div>

    <p v-if="!sid" class="empty mono">未绑定主 agent 会话（plan 自动派发与通知需要它）</p>
    <div v-else class="ps-card">
      <template v-if="session">
        <b class="ps-name">{{ session.title || session.agent }}</b>
        <span class="ps-state mono" :class="`ps-state--${session.state}`">{{ session.state }}</span>
        <span class="ps-beat mono" :title="fmtDateTime(session.last_seen_at)">最后心跳 {{ fmtAgo(session.last_seen_at) }}</span>
      </template>
      <span v-else-if="loadError" class="error mono">会话 {{ sid }} 读取失败：{{ loadError }}</span>
      <span v-else class="mono">加载中…</span>
      <code class="ps-sid mono">{{ sid }}</code>
      <button class="op-btn ps-open" type="button" @click="drawerOpen = true">打开会话</button>
    </div>

    <div v-if="sessionGone && !editing" class="ps-gone">
      <span class="mono">该会话已{{ session?.state === 'handed_off' ? '被接管' : '结束或离线' }}，plan 的派发与通知不会再送到它。</span>
      <button class="op-btn ps-rebind" type="button" :disabled="bindBusy" @click="rebindLatest">改绑到最近活跃会话</button>
      <button class="op-btn" type="button" :disabled="bindBusy" @click="startEdit">手动选择</button>
      <span v-if="rebindHint" class="mono ps-hint">{{ rebindHint }}</span>
    </div>
    <div v-if="editing" class="ps-edit">
      <select v-model="pick" class="op-input ps-select mono" aria-label="选择主 agent 会话">
        <option value="">（请选择会话）</option>
        <option v-if="sid && !candidates.some((s) => s.session_id === sid)" :value="sid">{{ sid }}（当前）</option>
        <option v-for="s in candidates" :key="s.session_id" :value="s.session_id">{{ sessionLabel(s) }}</option>
      </select>
      <button class="op-btn ps-save" type="button" :disabled="bindBusy || !pick || pick === sid" @click="bind(pick)">保存</button>
      <button class="op-btn" type="button" :disabled="bindBusy" @click="editing = false">取消</button>
    </div>
    <p v-if="bindError" class="error mono ps-bind-error">{{ bindError }}</p>

    <div v-if="sid" class="ps-send">
      <textarea v-model="draft" class="op-input ps-input" rows="2" placeholder="发给主 agent 的消息" />
      <div class="ps-send-row">
        <button class="op-btn ps-quick" type="button" @click="draft = HANDOFF_QUICK_PHRASE">{{ HANDOFF_QUICK_PHRASE }}</button>
        <button class="op-btn ps-send-btn" type="button" :disabled="!canSend" @click="send">{{ sending ? '发送中…' : '发给主 agent' }}</button>
      </div>
      <p v-if="sendResult" class="mono ps-result" :class="{ error: sendFailed }">{{ sendResult }}</p>
    </div>

    <SessionDrawer v-if="drawerOpen" :sid="sid" @close="drawerOpen = false" @changed="loadSession" @deleted="drawerOpen = false; emit('changed', '')" />
  </section>
</template>

<style scoped>
/* Same look as PlanDetail's sections: its scoped rules do not reach into this
   child component, so the shared classes are repeated here. */
.section { margin-top: 18px; }
.section-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.section-title { font-size: 12px; letter-spacing: 0.08em; color: var(--queue); text-transform: uppercase; margin: 0 0 10px; }
.op-btn { background: transparent; color: var(--phosphor); border: 1px solid var(--line); border-radius: var(--radius); padding: 4px 10px; font-size: 12px; cursor: pointer; }
.op-btn:hover:not(:disabled) { border-color: var(--phosphor); }
.op-btn:disabled { opacity: 0.55; cursor: default; }
.op-input { min-width: 0; background: var(--panel); color: var(--paper); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 8px; font-size: 12px; outline: none; }
.op-input:focus { border-color: var(--phosphor); }
.empty { padding: 12px 14px; text-align: center; color: var(--queue); font-size: 12px; border: 1px dashed var(--line); border-radius: var(--radius); }
.error { color: var(--fail); font-size: 12px; word-break: break-word; }
.ps-card { padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--panel); }
.ps-name { color: var(--paper); }
.ps-beat { color: var(--queue); font-size: 11px; }
.ps-gone { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; margin-top: 8px; padding: 8px 10px; border: 1px solid var(--warn, #d9822b); border-radius: var(--radius); font-size: 12px; color: var(--warn, #d9822b); }
.ps-hint { color: var(--queue); }
.ps-actions, .ps-edit, .ps-send-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.ps-card { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; margin-top: 8px; }
.ps-sid { opacity: 0.6; font-size: 11px; }
.ps-state { font-size: 11px; padding: 1px 6px; border: 1px solid var(--line, #8884); border-radius: 4px; }
.ps-state--waiting_reply, .ps-state--needs_attention { color: var(--warn, #d9822b); }
.ps-state--offline, .ps-state--ended { opacity: 0.6; }
.ps-edit, .ps-send { margin-top: 10px; }
.ps-select { min-width: 220px; max-width: 100%; }
.ps-input { width: 100%; box-sizing: border-box; margin-bottom: 6px; }
.ps-result { margin: 6px 0 0; font-size: 12px; }
</style>
