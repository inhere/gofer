// 统一对话时间线（设计稿 §2，计划 R12）。
//
// 工作台对话、job 详情「消息」、会话抽屉共用这一套渲染：人的消息与 agent 消息都用
// MarkdownBlock（marked + DOMPurify，见 MarkdownBlock 文件头），工具调用折成
// 「N 个工具调用」，gofer 注入的规则折成一行，交互卡内嵌，没发出去的那一轮单独提示。
//
// 输入是已经规整好的条目（utils/conversationTimeline.ts）：调用方只管把自己的数据转成
// 条目、给气泡抬头的措辞；折叠状态、间距、对齐都在这里。需要页面自己画的东西走插槽：
//   #custom  自己渲染一条（kind: 'custom'）
//   #extra   在气泡 / 提示行下面追加内容（送达状态、重试、撤销等）
import { ref } from 'vue'
import type { AttachmentRef } from '../api/types'
import { t } from '../i18n'
import MarkdownBlock from './MarkdownBlock.vue'
import AttachmentThumbs from './AttachmentThumbs.vue'
import InteractionCard from './InteractionCard.vue'
import StatusGlyph from '../ui/StatusGlyph.vue'
import Icon from '../ui/Icon.vue'
import { ChevronDown, ChevronRight } from '@lucide/vue'
import type {
  ConversationEntry,
  ConversationInteractionEntry,
  ConversationMessageEntry,
  ConversationUnsentEntry,
} from '../utils/conversationTimeline'
import { conversationEntryKey } from '../utils/conversationTimeline'

const props = defineProps<{
  entries: ConversationEntry[]
  // 超过这个字符数的消息默认折起（会话抽屉的老行为）；0 = 不折。
  clampChars?: number
}>()

const emit = defineEmits<{
  (e: 'answer', entry: ConversationInteractionEntry, value: string): void
  (e: 'punt', entry: ConversationInteractionEntry): void
  (e: 'resend', entry: ConversationUnsentEntry): void
}>()

// 工具调用组与注入规则默认折叠（设计稿 §2），展开状态按条目 id 记。
const openGroups = ref<Set<string>>(new Set())
const expandedText = ref<Set<string>>(new Set())

function isOpen(id: string): boolean {
  return openGroups.value.has(id)
}

function toggleGroup(id: string): void {
  const next = new Set(openGroups.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  openGroups.value = next
}

function isLong(entry: ConversationMessageEntry): boolean {
  const limit = props.clampChars ?? 0
  return limit > 0 && entry.text.length > limit
}

function isClamped(entry: ConversationMessageEntry): boolean {
  return isLong(entry) && !expandedText.value.has(entry.id)
}

function toggleText(id: string): void {
  const next = new Set(expandedText.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedText.value = next
}

function attachmentsOf(entry: ConversationMessageEntry): AttachmentRef[] {
  return entry.attachments ?? []
}
</script>

<template>
  <div class="ct" data-test="conversation-timeline">
    <template v-for="entry in entries" :key="conversationEntryKey(entry)">
      <!-- 人的消息 / agent 消息：都按 markdown 渲染（附图上移到气泡上方，与工作台一致） -->
      <div
        v-if="entry.kind === 'user' || entry.kind === 'agent'"
        class="msg"
        :class="entry.kind === 'user' ? 'msg--me' : 'msg--agent'"
        :data-kind="entry.kind"
      >
        <div class="who">
          <span class="who-name">{{ entry.label }}</span>
          <span v-if="entry.time" class="muted" :title="entry.timeTitle">{{ entry.time }}</span>
          <span v-if="entry.omitted" class="gap">{{ t('conversation.earlierOmitted') }}</span>
          <RouterLink
            v-if="entry.jobId"
            class="who-job mono"
            :to="`/jobs/${encodeURIComponent(entry.jobId)}`"
          >{{ entry.jobId.slice(-8) }}</RouterLink>
        </div>
        <div v-if="entry.kind === 'user'" class="bubble bubble--user msg-body">
          <div class="clamp" :class="{ clamped: isClamped(entry) }">
            <MarkdownBlock :text="entry.text" />
          </div>
          <AttachmentThumbs v-if="attachmentsOf(entry).length > 0" :items="attachmentsOf(entry)" />
          <span v-if="entry.truncated" class="truncated">{{ t('conversation.truncated') }}</span>
        </div>
        <div v-else class="bubble bubble--agent msg-body">
          <div class="clamp" :class="{ clamped: isClamped(entry) }">
            <MarkdownBlock :text="entry.text" />
          </div>
          <AttachmentThumbs v-if="attachmentsOf(entry).length > 0" :items="attachmentsOf(entry)" />
          <span v-if="entry.truncated" class="truncated">{{ t('conversation.truncated') }}</span>
        </div>
        <button
          v-if="isLong(entry)"
          class="link-btn mono ct-expand"
          type="button"
          @click="toggleText(entry.id)"
        >
          {{ expandedText.has(entry.id) ? t('conversation.collapse') : t('conversation.expand') }}
        </button>
        <slot name="extra" :entry="entry" />
      </div>

      <!-- gofer 注入的规则：默认只露一行 -->
      <div v-else-if="entry.kind === 'injected'" class="injected" data-test="timeline-injected">
        <button
          class="injected-sum mono"
          type="button"
          :aria-expanded="isOpen(entry.id)"
          @click="toggleGroup(entry.id)"
        >
          <Icon class="caret" :icon="isOpen(entry.id) ? ChevronDown : ChevronRight" size="sm" />
          <span>{{ isOpen(entry.id) ? t('conversation.injectedHide') : t('conversation.injectedShow') }}</span>
        </button>
        <pre v-if="isOpen(entry.id)" class="injected-text mono">{{ entry.text }}</pre>
      </div>

      <!-- 连续的工具调用：默认折成一行 -->
      <div v-else-if="entry.kind === 'tools'" class="tools" :data-open="isOpen(entry.id)">
        <button
          class="tools-sum"
          type="button"
          :aria-expanded="isOpen(entry.id)"
          data-test="tools-toggle"
          @click="toggleGroup(entry.id)"
        >
          <Icon class="caret" :icon="isOpen(entry.id) ? ChevronDown : ChevronRight" size="sm" />
          <span>{{ t('conversation.tools', { n: entry.calls.length }) }}</span>
          <span v-if="entry.failed > 0" class="tools-fail">{{ t('conversation.toolsFail', { n: entry.failed }) }}</span>
        </button>
        <div v-if="isOpen(entry.id)" class="tool-list">
          <div v-for="call in entry.calls" :key="call.id" class="tool">
            <StatusGlyph :status="call.status" :size="12" />
            <span class="tool-kind">{{ call.title }}</span>
            <span class="tool-arg" :title="call.summary">{{ call.summary }}</span>
          </div>
        </div>
      </div>

      <!-- ACP 权限决策（已决）：内嵌只读小卡 -->
      <div v-else-if="entry.kind === 'permission'" class="inline-approval done" data-test="inline-permission">
        <div class="ia-head">
          <StatusGlyph :family="entry.outcome === 'denied' ? 'fail' : 'done'" :size="13" />
          <span>{{ t(`conversation.permission.${entry.outcome}`) }}</span>
          <span v-if="entry.title" class="ia-title">{{ entry.title }}</span>
        </div>
        <div v-if="entry.reason" class="ia-reason">{{ entry.reason }}</div>
      </div>

      <!-- 交互卡（待作答 / 已作答）：作答由父组件处理 -->
      <InteractionCard
        v-else-if="entry.kind === 'interaction'"
        :interaction="entry.interaction"
        :submitting="entry.submitting"
        :readonly="entry.readonly"
        @answer="emit('answer', entry, $event)"
        @punt="emit('punt', entry)"
      />

      <!-- 这一轮没发出去：原消息留着，点「重发」填回输入框 -->
      <div v-else-if="entry.kind === 'unsent'" class="unsent" data-test="timeline-unsent">
        <div class="unsent-head mono">
          <span class="unsent-badge">{{ t('conversation.unsent') }}</span>
          <span class="unsent-why">{{ entry.text }}</span>
        </div>
        <pre v-if="entry.prompt" class="unsent-text msg-body">{{ entry.prompt }}</pre>
        <button
          class="btn sm unsent-resend"
          type="button"
          data-test="timeline-resend"
          :disabled="!entry.prompt"
          @click="emit('resend', entry)"
        >{{ t('conversation.resend') }}</button>
      </div>

      <!-- 调用方自己渲染的一条（会话抽屉的终端轮次） -->
      <slot v-else name="custom" :entry="entry" />
    </template>
  </div>
</template>

<style scoped>
.ct {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
  min-width: 0;
}
.msg {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  max-width: min(88%, 760px);
  min-width: 0;
}
.msg--me {
  align-self: flex-end;
  align-items: flex-end;
}
.msg--agent {
  align-self: flex-start;
}
.who {
  display: flex;
  align-items: center;
  gap: var(--s2);
  color: var(--ink-2);
  font-size: var(--fs-sm);
  font-weight: 500;
}
.muted {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-weight: 400;
}
.gap {
  color: var(--run);
  font-size: var(--fs-xs);
  font-weight: 400;
}
.who-job {
  color: var(--accent);
  font-size: var(--fs-xs);
  font-weight: 400;
}
.who-job:hover {
  text-decoration: underline;
}
/* 气泡只定外观；长内容不撑宽容器（min-width / 断行 / pre 与表格横滚）由 .msg-body 统一处理。 */
.bubble {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  padding: var(--s2) var(--s3);
  background: var(--surface);
  color: var(--ink);
  min-width: 0;
}
.bubble--user {
  background: var(--accent-soft);
}
.clamp {
  min-width: 0;
}
/* 折起：高度裁剪 + 渐隐，不截断 markdown 源码（语法不会被破坏）。 */
.clamp.clamped {
  max-height: 320px;
  overflow: hidden;
  -webkit-mask-image: linear-gradient(180deg, #000 78%, transparent 100%);
  mask-image: linear-gradient(180deg, #000 78%, transparent 100%);
}
.ct-expand {
  align-self: flex-start;
  background: none;
  border: none;
  padding: 0;
  color: var(--accent);
  font-size: var(--fs-xs);
  cursor: pointer;
}
.ct-expand:hover {
  text-decoration: underline;
}
.truncated {
  display: inline-block;
  margin-top: 5px;
  color: var(--time);
  font-size: var(--fs-xs);
}
.injected {
  align-self: flex-start;
  max-width: min(88%, 760px);
  min-width: 0;
}
.injected-sum {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  padding: 2px var(--s2);
  background: transparent;
  border: 1px dashed var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink-3);
  font: inherit;
  font-size: var(--fs-xs);
  cursor: pointer;
}
.injected-sum:hover {
  color: var(--ink);
  border-color: var(--line-strong);
}
.injected-sum:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.injected-text {
  margin: var(--s1) 0 0;
  max-height: 320px;
  overflow: auto;
  padding: var(--s2);
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  white-space: pre-wrap;
  word-break: break-word;
}
.tools {
  align-self: flex-start;
  max-width: min(88%, 760px);
  min-width: 0;
}
.tools-sum {
  display: inline-flex;
  align-items: center;
  gap: var(--s1);
  padding: var(--s1) var(--s2);
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink-2);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
}
.tools-sum:hover {
  color: var(--ink);
  border-color: var(--line-strong);
}
.tools-sum:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.tools-fail {
  color: var(--fail);
}
.tool-list {
  margin-top: var(--s1);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--sunken);
  overflow: hidden;
}
.tool {
  display: grid;
  grid-template-columns: auto auto minmax(0, 1fr);
  gap: var(--s2);
  align-items: center;
  padding: 5px var(--s2);
  color: var(--ink-2);
  font-size: var(--fs-sm);
}
.tool + .tool {
  border-top: 1px solid var(--line);
}
.tool-kind {
  font-weight: 500;
  white-space: nowrap;
}
.tool-arg {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink-3);
  font-family: var(--font-mono);
  font-size: var(--fs-xs);
}
.inline-approval {
  align-self: flex-start;
  max-width: min(88%, 760px);
  padding: var(--s2) var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-left: 3px solid var(--need);
  border-radius: var(--r-panel);
}
.inline-approval.done {
  border-left-color: var(--done);
}
.ia-head {
  display: flex;
  align-items: center;
  gap: var(--s1);
  color: var(--ink);
  font-size: var(--fs-sm);
  font-weight: 500;
}
.ia-title {
  color: var(--ink-3);
  font-weight: 400;
}
.ia-reason {
  margin-top: var(--s1);
  color: var(--ink-2);
  font-size: var(--fs-sm);
}
/* 未发送：用失败色标出来，原消息照抄一份，右侧给「重发」。 */
.unsent {
  align-self: stretch;
  padding: var(--s2) var(--s3);
  background: var(--surface);
  border: 1px solid var(--fail);
  border-left-width: 3px;
  border-radius: var(--r-panel);
}
.unsent-head {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: var(--s2);
}
.unsent-badge {
  color: var(--fail);
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.unsent-why {
  color: var(--ink-2);
  font-size: var(--fs-sm);
}
.unsent-text {
  margin: var(--s2) 0 0;
  max-height: 160px;
  overflow: auto;
  padding: var(--s2);
  background: var(--accent-soft);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink);
  font-family: inherit;
  font-size: var(--fs-sm);
  white-space: pre-wrap;
}
.unsent-resend {
  margin-top: var(--s2);
}
@media (max-width: 760px) {
  .msg,
  .tools,
  .inline-approval,
  .injected {
    max-width: 100%;
  }
}
</style>
