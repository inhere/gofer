<script setup lang="ts">
// Composer：统一的消息输入框（设计稿 §8）。
//  - 发送 / 排队（按会话持久化到 localStorage）/ 打断后发送：只有调用方声明支持时才显示对应按钮；
//  - 附图：给了 attachCapability 才显示附图条与 📎（AttachmentBar，能力位由调用方决定）；
//  - 急件：给了 urgentOption 才显示（信箱回信这类可以标急件的调用方）；
//  - 快捷键：Ctrl/⌘+Enter 总是发送；工作台按回车即发送（submitOnEnter）；
//  - 自适应高度、手机友好（点按目标 ≥40px）、Esc 不吞（抽屉自己处理）。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Paperclip } from '@lucide/vue'
import { t } from '../i18n'
import Icon from './Icon.vue'
import AttachmentBar from '../components/AttachmentBar.vue'
import type { AttachmentCapability } from '../utils/attachments'
import {
  autosizeComposer,
  clearComposerQueue,
  isComposerSubmit,
  readComposerQueue,
  writeComposerQueue,
} from './composer'

export type ComposerMode = 'send' | 'queue' | 'interrupt'

const props = withDefaults(
  defineProps<{
    /** 草稿（v-model） */
    modelValue: string
    placeholder?: string
    /** 输入框整体禁用（会话已结束等），仍显示说明 */
    disabled?: boolean
    /** 正在发送（按钮禁用并显示发送中） */
    sending?: boolean
    /** 现在能不能直接发送；false 时排队 / 打断后发送接管 */
    canSend?: boolean
    /** 能不能打断当前回合（只有声明了 interrupt 模式的调用方需要） */
    canInterrupt?: boolean
    /** 显示哪些发送方式，默认只显示发送 */
    modes?: ComposerMode[]
    /** 待发队列的作用域（localStorage 键 `gofer.<scope>.queue.<queueKey>`） */
    queueScope?: string
    /** 队列归属（会话 / 线程 id）；空串 = 不持久化 */
    queueKey?: string
    /** 发送按钮文字 */
    sendLabel?: string
    /** 回车即发送（工作台）；Ctrl/⌘+Enter 不受影响 */
    submitOnEnter?: boolean
    /** 显示「急件」勾选 */
    urgentOption?: boolean
    /** 急件（v-model:urgent） */
    urgent?: boolean
    /** 显示附图条（需要 attachCapability） */
    attachments?: boolean
    attachCapability?: AttachmentCapability
    /** 输入框下方的说明文字 */
    hint?: string
    /** 手机档把说明收进「?」（默认展开） */
    hintCollapsible?: boolean
    /** 挂载后聚焦 */
    focusOnMount?: boolean
    /** 无障碍名称 */
    ariaLabel?: string
  }>(),
  {
    placeholder: '',
    disabled: false,
    sending: false,
    canSend: true,
    canInterrupt: false,
    modes: () => ['send'],
    queueScope: 'composer',
    queueKey: '',
    sendLabel: '',
    submitOnEnter: false,
    urgentOption: false,
    urgent: false,
    attachments: false,
    attachCapability: undefined,
    hint: '',
    hintCollapsible: false,
    focusOnMount: false,
    ariaLabel: '',
  },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'update:urgent', value: boolean): void
  /** 现在发送（调用方负责清空 v-model / 清附件） */
  (e: 'send', text: string): void
  /** 打断当前回合；调用方取消后 Composer 会在可发送时自动发出队首 */
  (e: 'interrupt'): void
  (e: 'attachment-error', message: string): void
}>()

const root = ref<HTMLElement | null>(null)
const textarea = ref<HTMLTextAreaElement | null>(null)
const attachBar = ref<InstanceType<typeof AttachmentBar> | null>(null)
const draft = computed({
  get: () => props.modelValue,
  set: (value: string) => emit('update:modelValue', value),
})
const hintOpen = ref(!props.hintCollapsible)
const showModes = computed(() => props.modes.length > 1)
const mode = ref<ComposerMode>(props.modes.includes('send') ? 'send' : props.modes[0] ?? 'send')
const queued = ref<string[]>([])
const attachBlocked = computed(() => !!attachBar.value && !attachBar.value.canSend())
// 已选附件：紧凑附件条只在有图时占位（没图时它高度 0，但仍挂载：选文件 / 拖放靠它）。
const attachCount = computed(() => attachBar.value?.items?.length ?? 0)
// 附件提示：能力不可用时直接取 capability.reason（此时缩略图条根本不渲染，拿不到子组件的 hint），
// 其余情况（上传中 / 失败）用附件条的 hint。两边同一来源（utils/attachments 的 composerAttachmentState）。
const attachHint = computed(() => {
  if (!props.attachments || !props.attachCapability) return ''
  if (!props.attachCapability.enabled) return props.attachCapability.reason
  return attachBar.value?.hint() ?? ''
})
const submitDisabled = computed(() => {
  if (props.disabled || props.sending || attachBlocked.value || !draft.value.trim()) return true
  return mode.value === 'send' && !props.canSend
})

function loadQueue(): void {
  queued.value = props.queueKey ? readComposerQueue(props.queueScope, props.queueKey) : []
}

function persistQueue(items: string[]): void {
  if (!props.queueKey) return
  if (items.length === 0) clearComposerQueue(props.queueScope, props.queueKey)
  else writeComposerQueue(props.queueScope, props.queueKey, items)
}

watch(() => props.queueKey, loadQueue, { immediate: true })
watch(queued, (items) => persistQueue(items), { deep: true })

// 队列只管「现在发不了」的消息：一有空档就按顺序发出（打断后发送的那条在队首）。
watch(
  () => props.canSend,
  (ready) => {
    if (!ready || props.sending || queued.value.length === 0) return
    const [next, ...rest] = queued.value
    queued.value = rest
    emit('send', next)
  },
)

function clearQueue(): void {
  queued.value = []
  persistQueue([])
}

function submit(): void {
  const text = draft.value.trim()
  if (!text || props.disabled || props.sending) return
  if (mode.value === 'queue' && !props.canSend) {
    queued.value = [...queued.value, text]
    draft.value = ''
    return
  }
  if (mode.value === 'interrupt' && !props.canSend) {
    if (!props.canInterrupt) return
    queued.value = [text, ...queued.value]
    draft.value = ''
    emit('interrupt')
    return
  }
  if (!props.canSend || attachBlocked.value) return
  emit('send', text)
}

function onKeydown(event: KeyboardEvent): void {
  if (!isComposerSubmit(event, props.submitOnEnter)) return
  event.preventDefault()
  submit()
}

function onInput(event: Event): void {
  const el = event.target as HTMLTextAreaElement
  autosizeComposer(el, window.matchMedia?.('(max-width: 760px)').matches ? 0.28 : 0.4)
}

function onPaste(event: ClipboardEvent): void {
  attachBar.value?.addFromPaste(event)
}

function focus(): void {
  textarea.value?.focus()
}

function clearAttachments(): void {
  attachBar.value?.clear()
}

function readyIDs(): string[] {
  return attachBar.value?.readyIDs() ?? []
}

function openPicker(): void {
  if (props.attachments && props.attachCapability?.enabled) attachBar.value?.openPicker()
}

defineExpose({ focus, clearAttachments, readyIDs, openPicker, attachHint, attachBlocked })

// 窗口变宽 / 转屏时重新按内容撑开（不在 textarea 自身上挂 ResizeObserver：改高度会再触发观察）。
function resize(): void {
  if (textarea.value) autosizeComposer(textarea.value, window.matchMedia?.('(max-width: 760px)').matches ? 0.28 : 0.4)
}

onMounted(() => {
  if (props.focusOnMount) void nextTick(() => textarea.value?.focus())
  window.addEventListener('resize', resize)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resize)
})
</script>

<template>
  <div
    ref="root"
    class="composer-ui"
    :class="{ 'composer-ui--disabled': disabled }"
    data-test="composer"
    @dragover="attachBar?.handleDragOver($event)"
    @drop="attachBar?.handleDrop($event)"
  >
    <div
      v-if="attachments && attachCapability"
      class="composer-attach"
      :class="{ 'composer-attach--empty': attachCount === 0 }"
    >
      <AttachmentBar
        ref="attachBar"
        :capability="attachCapability"
        compact
        @error="emit('attachment-error', $event)"
      />
    </div>
    <div class="composer-box">
      <textarea
        ref="textarea"
        v-model="draft"
        class="composer-input"
        rows="1"
        :disabled="disabled"
        :placeholder="placeholder"
        :aria-label="ariaLabel || placeholder || t('composer.ariaLabel')"
        data-test="composer-input"
        @keydown="onKeydown"
        @input="onInput"
        @paste="onPaste"
      ></textarea>
      <div class="composer-row">
        <button
          v-if="attachments && attachCapability"
          class="composer-icon"
          type="button"
          :title="attachCapability.enabled ? t('composer.attach') : attachCapability.reason"
          :aria-label="t('composer.attach')"
          :disabled="!attachCapability.enabled"
          data-test="composer-attach"
          @click="openPicker()"
        ><Icon :icon="Paperclip" size="sm" /></button>
        <div v-if="showModes" class="composer-modes" role="group" :aria-label="t('composer.modeLabel')">
          <button
            v-for="item in modes"
            :key="item"
            type="button"
            class="composer-mode"
            :aria-pressed="mode === item"
            :class="{ 'composer-mode--on': mode === item }"
            :title="t(`composer.mode.${item}Title`)"
            :data-mode="item"
            @click="mode = item"
          >{{ t(`composer.mode.${item}`) }}</button>
        </div>
        <label v-if="urgentOption" class="composer-urgent" :title="t('composer.urgentTitle')">
          <input
            type="checkbox"
            :checked="urgent"
            data-test="composer-urgent"
            @change="emit('update:urgent', ($event.target as HTMLInputElement).checked)"
          />
          <span>{{ t('composer.urgent') }}</span>
        </label>
        <span v-if="queued.length > 0" class="composer-queued" data-test="composer-queued">
          {{ t('composer.queued', { n: queued.length }) }}
          <button type="button" class="composer-link" data-test="composer-clear-queue" @click="clearQueue">
            {{ t('composer.clearQueue') }}
          </button>
        </span>
        <span class="composer-grow"></span>
        <slot name="actions" />
        <button
          class="composer-send"
          type="button"
          :disabled="submitDisabled"
          data-test="composer-send"
          @click="submit"
        >{{ sending ? t('composer.sending') : sendLabel || t('composer.send') }}</button>
      </div>
    </div>
    <!-- 附件提示（不支持附图的原因 / 上传中 / 失败）：附件按钮自己的 title 之外再用一行小字，
         原来那条旧附件栏删掉后这句提示不丢。 -->
    <p v-if="attachments && attachHint" class="composer-attach-hint" data-test="composer-attach-hint">
      {{ attachHint }}
    </p>
    <div v-if="hint" class="composer-foot">
      <button
        v-if="hintCollapsible"
        class="composer-hint-toggle"
        type="button"
        :aria-expanded="hintOpen"
        :aria-label="hintOpen ? t('composer.hintHide') : t('composer.hintShow')"
        data-test="composer-hint-toggle"
        @click="hintOpen = !hintOpen"
      >?</button>
      <span v-show="hintOpen" class="composer-hint">{{ hint }}</span>
    </div>
  </div>
</template>

<style scoped>
.composer-ui {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  min-width: 0;
}
.composer-attach {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
/* 还没选附件：这一行不占位（也不吃掉父容器的 gap），附件条的 DOM 仍在。 */
.composer-attach--empty {
  display: none;
}
/* 附件提示一行小字：与输入的说明文字同口径，不抢正文。 */
.composer-attach-hint {
  margin: 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  line-height: 1.4;
}
.composer-box {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  padding: var(--s2);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
}
.composer-box:focus-within {
  border-color: var(--accent);
}
.composer-input {
  width: 100%;
  min-height: 36px;
  max-height: 40vh;
  padding: 2px;
  background: transparent;
  color: var(--ink);
  border: 0;
  outline: none;
  font: inherit;
  font-size: var(--fs-sm);
  line-height: 1.5;
  resize: none;
  overflow-y: auto;
}
.composer-input::placeholder {
  color: var(--ink-3);
}
.composer-input:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.composer-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--s1);
  min-width: 0;
}
.composer-icon {
  min-width: 28px;
  min-height: 28px;
  padding: 0 4px;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--r-ctl);
  color: var(--ink-2);
  font: inherit;
  cursor: pointer;
}
.composer-icon:hover:not(:disabled) {
  border-color: var(--line-strong);
  color: var(--ink);
}
.composer-icon:disabled {
  opacity: 0.4;
  cursor: default;
}
.composer-icon:focus-visible,
.composer-mode:focus-visible,
.composer-send:focus-visible,
.composer-link:focus-visible,
.composer-hint-toggle:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.composer-modes {
  display: inline-flex;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  overflow: hidden;
}
.composer-mode {
  padding: 3px var(--s2);
  background: transparent;
  border: 0;
  border-right: 1px solid var(--line);
  color: var(--ink-3);
  font: inherit;
  font-size: var(--fs-xs);
  cursor: pointer;
}
.composer-mode:last-child {
  border-right: 0;
}
.composer-mode--on {
  background: var(--accent-soft);
  color: var(--accent);
}
.composer-urgent {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--ink-2);
  font-size: var(--fs-xs);
  cursor: pointer;
  user-select: none;
}
.composer-urgent input {
  accent-color: var(--need);
  margin: 0;
}
.composer-queued {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px var(--s2);
  border: 1px solid var(--line);
  border-radius: 999px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
}
.composer-link {
  padding: 0;
  background: transparent;
  border: 0;
  color: var(--accent);
  font: inherit;
  font-size: inherit;
  cursor: pointer;
}
.composer-grow {
  flex: 1;
}
.composer-send {
  min-height: 30px;
  padding: 0 var(--s3);
  background: var(--accent);
  color: var(--accent-ink);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
  font-weight: 600;
  cursor: pointer;
}
.composer-send:disabled {
  opacity: 0.45;
  cursor: default;
}
.composer-foot {
  display: flex;
  align-items: flex-start;
  gap: var(--s1);
  min-width: 0;
}
.composer-hint {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  line-height: 1.4;
}
.composer-hint-toggle {
  flex: none;
  width: 18px;
  height: 18px;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: 50%;
  background: transparent;
  color: var(--ink-3);
  font-size: 11px;
  line-height: 16px;
  cursor: pointer;
}
@media (max-width: 760px) {
  .composer-send,
  .composer-icon,
  .composer-mode {
    min-height: 40px;
  }
  .composer-icon,
  .composer-mode {
    min-width: 40px;
  }
  .composer-input {
    font-size: var(--fs-input-mobile);
    max-height: 28vh;
  }
  .composer-row {
    gap: var(--s2);
  }
  .composer-queued {
    min-height: 32px;
  }
}
</style>
