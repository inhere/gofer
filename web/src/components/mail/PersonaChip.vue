<script setup lang="ts">
// 人物卡（design 2026-10-20-mail-conversations-ui-design.md 决策 2）：信箱的对话列表与
// 时间线共用同一张卡——名字、副行（agent · 项目 · 执行机）、状态点、按 id 稳定取色的首字头像；
// 悬停显示完整地址 / cwd / 最后活动时间；会话可点击打开会话抽屉（由外层挂 SessionDrawer）。
// 规则（取色、名字回退、状态文案）都在 utils/mailConversations.ts。
import { computed } from 'vue'
import type { MailParty } from '../../api/types'
import {
  personaColor,
  personaInitial,
  personaLabel,
  personaSeed,
  personaState,
  personaStateLabel,
  personaStateTone,
  personaSubline,
  personaTooltip,
} from '../../utils/mailConversations'

const props = withDefaults(
  defineProps<{
    party: MailParty
    // row：列表行里的窄版（只有头像、名字、状态点）；full：时间线 / 对话头（多一副行与状态词）。
    variant?: 'row' | 'full'
    nowSec?: number
  }>(),
  { variant: 'full', nowSec: 0 },
)
const emit = defineEmits<{ (e: 'open-session', sid: string): void }>()

const name = computed(() => personaLabel(props.party))
const color = computed(() => personaColor(personaSeed(props.party)))
const initial = computed(() => personaInitial(name.value))
const subline = computed(() => personaSubline(props.party))
const state = computed(() => personaState(props.party))
const tip = computed(() => personaTooltip(props.party, props.nowSec))
const sid = computed(() => (props.party.kind === 'session' ? (props.party.id ?? '') : ''))

function open(): void {
  if (sid.value) emit('open-session', sid.value)
}

// 人物卡常嵌在可点的一行里（对话列表整行就是一个 button），根元素用 span + role 而不是
// button，避开嵌套按钮这种无效结构。点会话名字只开抽屉，不再让外层行把它当成「选这个对话」。
function onClick(e: MouseEvent): void {
  if (!sid.value) return
  e.stopPropagation()
  open()
}

function onKey(e: KeyboardEvent): void {
  if (!sid.value) return
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    open()
  }
}
</script>

<template>
  <span
    class="persona"
    :class="[`persona--${variant}`, { 'persona--link': sid }]"
    :role="sid ? 'button' : undefined"
    :tabindex="sid ? 0 : undefined"
    :title="tip"
    data-test="mail-persona"
    :data-persona-variant="variant"
    @click="onClick"
    @keydown="onKey"
  >
    <span class="persona-avatar" :style="{ background: color }">{{ initial }}</span>
    <span class="persona-text">
      <span class="persona-name" data-test="mail-persona-name">{{ name }}</span>
      <span v-if="variant === 'full' && subline" class="persona-sub dim" data-test="mail-persona-sub">{{ subline }}</span>
    </span>
    <span v-if="state" class="persona-state" :class="`tone--${personaStateTone(state)}`" :title="personaStateLabel(state)">
      <span class="persona-dot" aria-hidden="true"></span>
      <span v-if="variant === 'full'" class="persona-state-label">{{ personaStateLabel(state) }}</span>
    </span>
  </span>
</template>

<style scoped>
.persona {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  background: none;
  border: 0;
  padding: 0;
  color: var(--paper);
  font-family: var(--font-sans);
  font-size: 12px;
  text-align: left;
}
.persona--link {
  cursor: pointer;
}
.persona--link:hover .persona-name {
  color: var(--phosphor);
  text-decoration: underline;
}
.persona-avatar {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  color: #fff;
  font-size: 11px;
  font-weight: 700;
  line-height: 1;
}
.persona--full .persona-avatar {
  width: 26px;
  height: 26px;
  font-size: 13px;
}
.persona-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.persona-name {
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 22ch;
}
.persona--full .persona-name {
  max-width: 34ch;
}
.persona-sub {
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 36ch;
}
.persona-state {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
}
.persona-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: currentColor;
}
.tone--run {
  color: var(--run);
}
.tone--done {
  color: var(--done);
}
.tone--fail {
  color: var(--fail);
}
.tone--queue {
  color: var(--queue);
}
.dim {
  color: var(--queue);
}
</style>
