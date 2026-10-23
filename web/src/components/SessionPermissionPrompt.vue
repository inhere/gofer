<script setup lang="ts">
// 会话抽屉里的终端工具授权请求（Claude Code PermissionRequest）：等待中显示
// 允许 / 总是允许 / 拒绝 / 附原因拒绝；已结束显示结局。只有会话本人能答（后端校验）。
// gofer-02fu：卡片带 questions 时是终端 agent 的选择题（AskUserQuestion），按题作答。
import { computed, reactive, ref } from 'vue'
import { answerSessionPermission } from '../api/client'
import type { Decision } from '../api/types'
import { fmtAgo, fmtDateTime } from '../api/time'
import {
  denyAnswer,
  permissionAnswerText,
  permissionChoices,
  permissionReleasedText,
  questionAnswer,
  questionAnswerText,
} from '../utils/sessionPermission'

const props = defineProps<{ sid: string; decision: Decision; nowSec: number }>()
const emit = defineEmits<{ (e: 'answered', d: Decision): void }>()

const busy = ref(false)
const error = ref('')
const inputOpen = ref(false)
const denyOpen = ref(false)
const reason = ref('')

const open = computed(() => props.decision.state === 'OPEN')
const summary = computed(() => props.decision.permission?.summary || props.decision.question)
const choices = computed(() => permissionChoices(props.decision))
const questions = computed(() => props.decision.permission?.questions ?? [])
const picked = reactive<Record<string, string[]>>({})
const other = reactive<Record<string, string>>({})
const questionReply = computed(() => questionAnswer(questions.value, picked, other))

function toggle(q: string, label: string, multi: boolean): void {
  const cur = picked[q] ?? []
  if (multi) {
    picked[q] = cur.includes(label) ? cur.filter((l) => l !== label) : [...cur, label]
  } else {
    picked[q] = [label]
  }
}

async function answer(value: string): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const d = await answerSessionPermission(props.sid, props.decision.id, value)
    denyOpen.value = false
    reason.value = ''
    emit('answered', d)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="perm" :class="{ 'perm--open': open }" data-test="session-permission">
    <div class="perm-head mono">
      <strong>{{ questions.length > 0 ? '等你回答' : '需要授权' }}</strong>
      <span :title="fmtDateTime(decision.asked_at)">{{ fmtAgo(decision.asked_at, nowSec) }}</span>
    </div>
    <code v-if="questions.length === 0" class="perm-sum" data-test="permission-summary">{{ summary }}</code>
    <form v-if="questions.length > 0 && open" class="perm-qs" data-test="session-question" @submit.prevent="questionReply && answer(questionReply)">
      <fieldset v-for="q in questions" :key="q.question" class="perm-q">
        <legend class="perm-q-title">
          <span v-if="q.header" class="perm-q-head mono">{{ q.header }}</span>{{ q.question }}
          <small v-if="q.multiSelect" class="mono">（可多选）</small>
        </legend>
        <div class="perm-acts">
          <button
            v-for="o in q.options ?? []"
            :key="o.label"
            class="perm-btn mono"
            :class="{ 'perm-btn--ok': (picked[q.question] ?? []).includes(o.label) }"
            type="button"
            :title="o.description"
            :disabled="busy"
            @click="toggle(q.question, o.label, !!q.multiSelect)"
          >{{ o.label }}</button>
        </div>
        <input v-model="other[q.question]" class="mono perm-other" placeholder="其他（自由填写，填了以它为准）" />
      </fieldset>
      <div class="perm-acts">
        <button class="perm-btn perm-btn--ok mono" type="submit" :disabled="busy || !questionReply" data-test="question-submit">提交回答</button>
      </div>
      <p class="perm-hint mono">终端在等这些回答；提交后 agent 直接拿到答案继续。</p>
      <p v-if="error" class="perm-err mono">{{ error }}</p>
    </form>
    <button v-if="decision.permission?.input && questions.length === 0" class="link-btn mono" type="button" @click="inputOpen = !inputOpen">
      {{ inputOpen ? '收起参数' : '查看参数' }}
    </button>
    <pre v-if="inputOpen" class="perm-input mono">{{ decision.permission?.input }}</pre>
    <template v-if="open && questions.length === 0">
      <div class="perm-acts">
        <button
          v-for="c in choices"
          :key="c.answer"
          class="perm-btn mono"
          :class="`perm-btn--${c.style}`"
          type="button"
          :disabled="busy"
          :data-test="`permission-${c.answer}`"
          @click="answer(c.answer)"
        >{{ c.label }}</button>
        <button class="perm-btn mono" type="button" :disabled="busy" data-test="permission-deny-note" @click="denyOpen = !denyOpen">
          附原因拒绝
        </button>
      </div>
      <form v-if="denyOpen" class="perm-deny" @submit.prevent="answer(denyAnswer(reason))">
        <input v-model="reason" class="mono" placeholder="拒绝原因（会告诉 agent）" data-test="permission-deny-reason" />
        <button class="perm-btn perm-btn--bad mono" type="submit" :disabled="busy">拒绝</button>
      </form>
      <p class="perm-hint mono">终端里的授权对话框同时在等；先答的一方生效。</p>
      <p v-if="error" class="perm-err mono">{{ error }}</p>
    </template>
    <div v-if="!open" class="perm-done mono" data-test="permission-outcome">
      <template v-if="decision.state === 'ANSWERED' && questions.length > 0"><span class="perm-ans">{{ questionAnswerText(decision) }}</span> · {{ decision.answered_by || 'web' }}</template>
      <template v-else-if="decision.state === 'ANSWERED'">{{ permissionAnswerText(decision) }} · {{ decision.answered_by || 'web' }}</template>
      <template v-else>{{ permissionReleasedText(decision) }}</template>
    </div>
  </div>
</template>

<style scoped>
.perm {
  align-self: stretch;
  border: 1px dashed var(--line);
  border-radius: 8px;
  padding: 8px 11px;
  font-size: 13px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.perm--open {
  border: 1px solid var(--run);
}
.perm-head {
  display: flex;
  gap: 8px;
  font-size: 11px;
  color: var(--ink-3);
}
.perm--open .perm-head strong {
  color: var(--run);
}
.perm-sum {
  white-space: pre-wrap;
  word-break: break-all;
}
.perm-input {
  max-height: 240px;
  overflow: auto;
  font-size: 11px;
  background: var(--sunken);
  padding: 6px;
  border-radius: var(--r-chip);
  margin: 0;
}
.perm-acts,
.perm-deny {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.perm-deny input {
  flex: 1;
  min-width: 160px;
}
.perm-btn {
  font-size: 12px;
  padding: 3px 9px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: transparent;
  color: inherit;
  cursor: pointer;
}
.perm-btn--ok {
  color: var(--done);
  border-color: var(--done);
}
.perm-btn--bad {
  color: var(--fail);
  border-color: var(--fail);
}
.perm-hint,
.perm-done {
  font-size: 11px;
  color: var(--ink-3);
  margin: 0;
}
.perm-qs {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.perm-q {
  border: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.perm-q-title {
  padding: 0;
  font-size: 13px;
}
.perm-q-head {
  font-size: 11px;
  color: var(--ink-3);
  margin-right: 6px;
}
.perm-other {
  min-width: 0;
  font-size: 12px;
}
.perm-ans {
  white-space: pre-line;
}
.perm-err {
  font-size: 11px;
  color: var(--fail);
  margin: 0;
}
</style>
