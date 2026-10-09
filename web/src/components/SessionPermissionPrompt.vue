<script setup lang="ts">
// 会话抽屉里的终端工具授权请求（Claude Code PermissionRequest）：等待中显示
// 允许 / 总是允许 / 拒绝 / 附原因拒绝；已结束显示结局。只有会话本人能答（后端校验）。
import { computed, ref } from 'vue'
import { answerSessionPermission } from '../api/client'
import type { Decision } from '../api/types'
import { fmtAgo, fmtDateTime } from '../api/time'
import { denyAnswer, permissionAnswerText, permissionChoices, permissionReleasedText } from '../utils/sessionPermission'

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
      <strong>需要授权</strong>
      <span :title="fmtDateTime(decision.asked_at)">{{ fmtAgo(decision.asked_at, nowSec) }}</span>
    </div>
    <code class="perm-sum" data-test="permission-summary">{{ summary }}</code>
    <button v-if="decision.permission?.input" class="link-btn mono" type="button" @click="inputOpen = !inputOpen">
      {{ inputOpen ? '收起参数' : '查看参数' }}
    </button>
    <pre v-if="inputOpen" class="perm-input mono">{{ decision.permission?.input }}</pre>
    <template v-if="open">
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
    <div v-else class="perm-done mono" data-test="permission-outcome">
      <template v-if="decision.state === 'ANSWERED'">{{ permissionAnswerText(decision) }} · {{ decision.answered_by || 'web' }}</template>
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
  color: var(--queue);
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
  background: var(--term-bg);
  padding: 6px;
  border-radius: var(--radius);
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
  border-radius: var(--radius);
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
  color: var(--queue);
  margin: 0;
}
.perm-err {
  font-size: 11px;
  color: var(--fail);
  margin: 0;
}
</style>
