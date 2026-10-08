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
const emit = defineEmits<{ (e: 'changed', sid: string): void }>()

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
