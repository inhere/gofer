<script setup lang="ts">
// 会话催办（N2 §E，SESS-12）：会话抽屉里的一个小区块。列出该会话的催办，并新建一条——
// 「每隔 N 分钟」或「停滞超过 N 分钟」+ 文本（+ 可选截止时间）。到点由 server 走与「发消息
// 给会话」相同的送达阶梯；连续送达失败 3 次会自动暂停（这里能看到原因并「恢复」）。
// 只有人（user / admin）能建、改；管家与 job 凭证不能。
import { computed, onMounted, ref, watch } from 'vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import {
  ApiError,
  createSessionNudge,
  deleteSessionNudge,
  listSessionNudges,
  setSessionNudgeState,
} from '../api/client'
import type { SessionNudge, SessionNudgeKind } from '../api/types'
import { fmtAgo } from '../api/time'
import {
  nudgeCanCreate,
  nudgeMinutesToSec,
  nudgeRuleLabel,
  nudgeStatusText,
  nudgeUntilToUnix,
} from '../utils/sessionNudge'
import { t } from '../i18n'

const props = defineProps<{ sid: string; state?: string }>()

const open = ref(false)
const nudges = ref<SessionNudge[]>([])
const loadError = ref('')
const actionError = ref('')
const busy = ref(false)
const kind = ref<SessionNudgeKind>('stalled')
const minutes = ref('20')
const text = ref('')
const until = ref('')

const canCreate = computed(() => nudgeCanCreate(props.state))
const liveCount = computed(() => nudges.value.filter((n) => n.state !== 'ended').length)

async function load(): Promise<void> {
  if (!props.sid) return
  try {
    const res = await listSessionNudges(props.sid, true)
    nudges.value = res.nudges ?? []
    loadError.value = ''
  } catch (e) {
    loadError.value = e instanceof ApiError || e instanceof Error ? e.message : String(e)
  }
}

async function act(fn: () => Promise<unknown>): Promise<void> {
  busy.value = true
  actionError.value = ''
  try {
    await fn()
    await load()
  } catch (e) {
    actionError.value = e instanceof ApiError || e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function add(): Promise<void> {
  const sec = nudgeMinutesToSec(minutes.value)
  const untilAt = nudgeUntilToUnix(until.value)
  if (sec == null) {
    actionError.value = t('sessions.nudge.errInterval')
    return
  }
  if (untilAt == null) {
    actionError.value = t('sessions.nudge.errUntil')
    return
  }
  if (!text.value.trim()) {
    actionError.value = t('sessions.nudge.errText')
    return
  }
  await act(async () => {
    await createSessionNudge(props.sid, {
      kind: kind.value,
      interval_sec: sec,
      text: text.value.trim(),
      ...(untilAt > 0 ? { until_at: untilAt } : {}),
    })
    text.value = ''
    until.value = ''
  })
}

// 催办状态随 `sessions` 主题推送刷新（server 写 nudge 行时会发 session 变更）。
const live = createLiveTopic('sessions', { initial: false, fetch: load })

watch(() => props.sid, () => void load())
onMounted(() => {
  void load()
  live.start()
})
</script>

<template>
  <section class="nudges mono" data-test="session-nudges">
    <button type="button" class="nudges-head" data-test="nudges-toggle" @click="open = !open">
      <strong>{{ t('sessions.nudge.title') }}</strong>
      <span class="dim">{{ liveCount > 0 ? t('sessions.nudge.count', { n: liveCount }) : t('sessions.nudge.none') }}</span>
      <span class="dim caret">{{ open ? t('util.action.collapse') : t('sessions.nudge.expand') }}</span>
    </button>
    <div v-if="open" class="nudges-body">
      <p v-if="loadError" class="nudge-err">{{ loadError }}</p>
      <ul v-if="nudges.length" class="nudge-list">
        <li v-for="n in nudges" :key="n.id" :class="['nudge-row', `nudge-row--${n.state}`]" data-test="nudge-row">
          <div class="nudge-main">
            <span class="nudge-rule">{{ nudgeRuleLabel(n.kind, n.interval_sec) }}</span>
            <span class="nudge-state">{{ nudgeStatusText(n) }}</span>
          </div>
          <div class="nudge-text" :title="n.text">{{ n.text }}</div>
          <div class="nudge-meta dim">
            {{ t('sessions.nudge.fired', { n: n.fire_count }) }}<template v-if="n.last_fired_at"> · {{ t('sessions.nudge.lastFired', { ago: fmtAgo(n.last_fired_at) }) }}</template>
            <template v-if="n.last_error"> · {{ n.last_error }}</template>
          </div>
          <div v-if="n.state !== 'ended'" class="nudge-acts">
            <button v-if="n.state === 'active'" type="button" class="act" :disabled="busy" data-test="nudge-pause"
              @click="act(() => setSessionNudgeState(n.id, 'paused'))">{{ t('sessions.nudge.pause') }}</button>
            <button v-else type="button" class="act" :disabled="busy || !canCreate" data-test="nudge-resume"
              @click="act(() => setSessionNudgeState(n.id, 'active'))">{{ t('sessions.nudge.resume') }}</button>
            <button type="button" class="act act--warn" :disabled="busy" data-test="nudge-delete"
              @click="act(() => deleteSessionNudge(n.id))">{{ t('util.action.delete') }}</button>
          </div>
        </li>
      </ul>
      <p v-else class="dim nudge-empty">{{ t('sessions.nudge.empty') }}</p>

      <form v-if="canCreate" class="nudge-form" data-test="nudge-form" @submit.prevent="add">
        <div class="nudge-form-row">
          <select v-model="kind" class="nudge-input" data-test="nudge-kind">
            <option value="stalled">{{ t('sessions.nudge.kindStalled') }}</option>
            <option value="every">{{ t('sessions.nudge.kindEvery') }}</option>
          </select>
          <input v-model="minutes" class="nudge-input nudge-minutes" type="number" min="1" step="1" data-test="nudge-minutes" />
          <span class="dim">{{ t('sessions.nudge.minutes') }}</span>
        </div>
        <input v-model="text" class="nudge-input nudge-textbox" type="text" maxlength="2000" :placeholder="t('sessions.nudge.textPlaceholder')" data-test="nudge-text" />
        <div class="nudge-form-row">
          <label class="dim">{{ t('sessions.nudge.until') }}<input v-model="until" class="nudge-input" type="datetime-local" data-test="nudge-until" /></label>
          <button type="submit" class="act act--primary" :disabled="busy" data-test="nudge-add">{{ t('sessions.nudge.add') }}</button>
        </div>
      </form>
      <p v-else class="dim nudge-empty">{{ t('sessions.nudge.cannotCreate') }}</p>
      <p v-if="actionError" class="nudge-err" data-test="nudge-error">{{ actionError }}</p>
      <p class="dim nudge-hint">{{ t('sessions.nudge.hint') }}</p>
    </div>
  </section>
</template>

<style scoped>
.nudges {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink);
  font-size: 11px;
}
.nudges-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 10px;
  background: transparent;
  border: 0;
  color: inherit;
  font: inherit;
  cursor: pointer;
  text-align: left;
}
.nudges-head .caret {
  margin-left: auto;
}
.nudges-body {
  padding: 0 10px 8px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.dim {
  color: var(--dim, var(--ink));
  opacity: 0.7;
}
.nudge-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.nudge-row {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 6px 8px;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.nudge-row--paused .nudge-state {
  color: var(--warn, var(--run));
}
.nudge-row--ended {
  opacity: 0.55;
}
.nudge-main {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: baseline;
}
.nudge-rule {
  color: var(--accent);
}
.nudge-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.nudge-acts {
  display: flex;
  gap: 6px;
}
.nudge-form {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.nudge-form-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.nudge-input {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 6px;
  font: inherit;
}
.nudge-minutes {
  width: 64px;
}
.nudge-textbox {
  width: 100%;
  box-sizing: border-box;
}
.nudge-err {
  color: var(--fail);
  margin: 0;
  word-break: break-word;
}
.nudge-empty,
.nudge-hint {
  margin: 0;
}
.act {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 10px;
  font-size: 11px;
  cursor: pointer;
}
.act:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.act:disabled {
  opacity: 0.45;
  cursor: default;
}
.act--primary {
  color: var(--accent);
  border-color: var(--accent);
}
.act--warn:hover:not(:disabled) {
  border-color: var(--fail);
  color: var(--fail);
}
</style>
