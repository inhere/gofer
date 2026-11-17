<script setup lang="ts">
// 会话抽屉里的终端工具授权请求（Claude Code PermissionRequest）：等待中显示
// 允许 / 总是允许 / 拒绝 / 附原因拒绝；已结束显示结局。只有会话本人能答（后端校验）。
// gofer-02fu：卡片带 questions 时是终端 agent 的选择题（AskUserQuestion），按题作答。
// T6 R11：卡片本体交给统一的 InteractionCard 渲染，本组件只做数据适配与作答调用。
import { computed, reactive, ref } from 'vue'
import { answerSessionPermission } from '../api/client'
import type { Decision } from '../api/types'
import { fmtAgo, fmtDateTime } from '../api/time'
import { t } from '../i18n'
import {
  permissionReleasedText,
  questionAnswer,
} from '../utils/sessionPermission'
import {
  permissionCardAnswer,
  permissionCardInteraction,
  questionCardInteractions,
  questionPickText,
} from '../utils/sessionPermissionCard'
import InteractionCard from './InteractionCard.vue'

const props = defineProps<{ sid: string; decision: Decision; nowSec: number }>()
const emit = defineEmits<{ (e: 'answered', d: Decision): void }>()

const busy = ref(false)
const error = ref('')

const open = computed(() => props.decision.state === 'OPEN')
const questions = computed(() => props.decision.permission?.questions ?? [])
// 投影成统一卡片的形状：权限请求一张卡；选择题每题一张卡。
const permissionCard = computed(() => permissionCardInteraction(props.decision))
const otherKnown = computed(() => (permissionCard.value.options ?? []).map((o) => o.value))
// 选择题按题作答，全部答完才提交 answers:<问题 → 文本>（终端 agent 收的就是这个形状）。
const picked = reactive<Record<string, string>>({})
const cards = computed(() =>
  questionCardInteractions(props.decision).map((card, i) => {
    const q = questions.value[i]
    const local = q ? picked[q.question] : ''
    return local ? { ...card, status: 'answered' as const, answer: local } : card
  }),
)

async function answer(value: string): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const d = await answerSessionPermission(props.sid, props.decision.id, value)
    emit('answered', d)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

// 权限请求：选项值原样作答；「其他」里的自由文本按「附原因拒绝」编码。
function onPermissionAnswer(value: string): void {
  const v = permissionCardAnswer(value, otherKnown.value)
  if (v !== '') {
    void answer(v)
  }
}

// 选择题：记下这一题的答案，全答完就提交。
function onQuestionAnswer(i: number, value: string): void {
  const q = questions.value[i]
  if (!q) return
  picked[q.question] = questionPickText(value, !!q.multiSelect)
  const reply = questionAnswer(questions.value, {}, picked)
  if (reply) {
    void answer(reply)
  }
}
</script>

<template>
  <div class="perm" :class="{ 'perm--open': open }" data-test="session-permission">
    <div class="perm-head mono">
      <strong>{{ questions.length > 0 ? t('sessions.perm.waiting') : t('sessions.perm.needsGrant') }}</strong>
      <span :title="fmtDateTime(decision.asked_at)">{{ fmtAgo(decision.asked_at, nowSec) }}</span>
    </div>
    <div v-if="questions.length > 0" class="perm-qs" data-test="session-question">
      <InteractionCard
        v-for="(card, i) in cards"
        :key="card.id"
        :interaction="card"
        :submitting="busy"
        :other-label="t('sessions.perm.other')"
        @answer="onQuestionAnswer(i, $event)"
      />
      <p v-if="open" class="perm-hint mono">{{ t('sessions.perm.questionHint') }}</p>
    </div>
    <template v-else>
      <InteractionCard
        :interaction="permissionCard"
        :submitting="busy"
        :other-label="t('sessions.perm.denyReasonPlaceholder')"
        @answer="onPermissionAnswer"
      />
      <!-- 没在 web 上作答就结束：说清是谁处理的（卡片本身只写「已过期」） -->
      <p v-if="!open && decision.state !== 'ANSWERED'" class="perm-done mono" data-test="permission-outcome">
        {{ permissionReleasedText(decision) }}
      </p>
    </template>
    <p v-if="error" class="perm-err mono">{{ error }}</p>
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
.perm-err {
  font-size: 11px;
  color: var(--fail);
  margin: 0;
}
</style>
