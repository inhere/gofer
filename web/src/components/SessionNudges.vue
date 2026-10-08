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
    actionError.value = '间隔至少 1 分钟'
    return
  }
  if (untilAt == null) {
    actionError.value = '截止时间无法解析'
    return
  }
  if (!text.value.trim()) {
    actionError.value = '请填写催办内容'
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
      <strong>催办</strong>
      <span class="dim">{{ liveCount > 0 ? `${liveCount} 条` : '无' }}</span>
      <span class="dim caret">{{ open ? '收起' : '展开' }}</span>
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
            已发 {{ n.fire_count }} 次<template v-if="n.last_fired_at"> · 上次 {{ fmtAgo(n.last_fired_at) }} 前</template>
            <template v-if="n.last_error"> · {{ n.last_error }}</template>
          </div>
          <div v-if="n.state !== 'ended'" class="nudge-acts">
            <button v-if="n.state === 'active'" type="button" class="act" :disabled="busy" data-test="nudge-pause"
              @click="act(() => setSessionNudgeState(n.id, 'paused'))">暂停</button>
            <button v-else type="button" class="act" :disabled="busy || !canCreate" data-test="nudge-resume"
              @click="act(() => setSessionNudgeState(n.id, 'active'))">恢复</button>
            <button type="button" class="act act--warn" :disabled="busy" data-test="nudge-delete"
              @click="act(() => deleteSessionNudge(n.id))">删除</button>
          </div>
        </li>
      </ul>
      <p v-else class="dim nudge-empty">还没有催办。</p>

      <form v-if="canCreate" class="nudge-form" data-test="nudge-form" @submit.prevent="add">
        <div class="nudge-form-row">
          <select v-model="kind" class="nudge-input" data-test="nudge-kind">
            <option value="stalled">停滞超过</option>
            <option value="every">每隔</option>
          </select>
          <input v-model="minutes" class="nudge-input nudge-minutes" type="number" min="1" step="1" data-test="nudge-minutes" />
          <span class="dim">分钟</span>
        </div>
        <input v-model="text" class="nudge-input nudge-textbox" type="text" maxlength="2000" placeholder="催办内容，如：进展如何？卡住了吗？" data-test="nudge-text" />
        <div class="nudge-form-row">
          <label class="dim">截止（可选）<input v-model="until" class="nudge-input" type="datetime-local" data-test="nudge-until" /></label>
          <button type="submit" class="act act--primary" :disabled="busy" data-test="nudge-add">添加催办</button>
        </div>
      </form>
      <p v-else class="dim nudge-empty">会话已结束或已被接管，不能新建催办。</p>
      <p v-if="actionError" class="nudge-err" data-test="nudge-error">{{ actionError }}</p>
      <p class="dim nudge-hint">到点走「发消息给会话」同一条送达路径；连续送达失败 3 次自动暂停。「停滞」＝运行中（或已停下但工作项未结）且超过该时长没有进展。</p>
    </div>
  </section>
</template>

<style scoped>
.nudges {
  flex: none;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--paper);
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
  color: var(--dim, var(--paper));
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
  border-radius: var(--radius);
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
  color: var(--phosphor);
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
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
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
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  cursor: pointer;
}
.act:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.act:disabled {
  opacity: 0.45;
  cursor: default;
}
.act--primary {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.act--warn:hover:not(:disabled) {
  border-color: var(--fail);
  color: var(--fail);
}
</style>
