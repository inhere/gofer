<script setup lang="ts">
// Toast：统一的提示条（设计稿「新组件 → 吸收的旧实现」：UndoToast / InteractionToast）。
//  - placement="corner"：右下角卡片（手机档挪到顶栏下方），标题 + 正文，可点正文跳转、可关闭；
//  - placement="bottom"：底部居中的细条，正文 + 计数 + 动作按钮（撤销窗口用）；
//  - duration > 0 时到点自己 emit('close')，标题 / 正文 / 时长变了重新计时；0 = 不自动收；
//  - role="status" aria-live="polite"：读屏软提示，不抢焦点。
// 组件不碰 router 与业务数据：点正文 emit('body')，点动作 emit('action')，由调用方处理。
import { onMounted, onUnmounted, watch } from 'vue'
import { t } from '../i18n'

const props = withDefaults(
  defineProps<{
    title?: string
    text?: string
    // 正文旁的小字（如撤销倒计时「5s」）
    meta?: string
    // 动作按钮文字；不给就没有按钮
    actionLabel?: string
    // 显示右上角 ×
    closable?: boolean
    // 正文可点（渲染成 button，emit body）
    bodyAction?: boolean
    tone?: 'neutral' | 'danger'
    placement?: 'corner' | 'bottom'
    // 自动消失的毫秒数；0 = 不自动收
    duration?: number
  }>(),
  {
    title: undefined,
    text: undefined,
    meta: undefined,
    actionLabel: undefined,
    closable: false,
    bodyAction: false,
    tone: 'neutral',
    placement: 'bottom',
    duration: 0,
  },
)
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'action'): void
  (e: 'body'): void
}>()

let timer: number | null = null

function clearTimer(): void {
  if (timer != null) {
    window.clearTimeout(timer)
    timer = null
  }
}

// 每次弹新的提示都要重新计时（同一条提示的正文变了也算新的）。
function schedule(): void {
  clearTimer()
  if (props.duration <= 0) return
  timer = window.setTimeout(() => {
    timer = null
    emit('close')
  }, props.duration)
}

onMounted(schedule)
onUnmounted(clearTimer)
watch(() => [props.title, props.text, props.duration], schedule)
</script>

<template>
  <div
    class="toast"
    :class="[`toast--${placement}`, `toast--${tone}`]"
    role="status"
    aria-live="polite"
    data-testid="toast"
  >
    <button v-if="closable" class="close" type="button" :aria-label="t('util.toast.close')" data-testid="toast-close" @click="emit('close')">
      ×
    </button>
    <component :is="bodyAction ? 'button' : 'div'" class="body" :type="bodyAction ? 'button' : undefined" data-testid="toast-body" @click="bodyAction && emit('body')">
      <span v-if="title" class="t1 mono">{{ title }}</span>
      <span v-if="text" class="tx mono">{{ text }}</span>
      <slot />
    </component>
    <span v-if="meta" class="meta mono" data-testid="toast-meta">{{ meta }}</span>
    <button v-if="actionLabel" class="act mono" type="button" data-testid="toast-action" @click="emit('action')">
      {{ actionLabel }}
    </button>
  </div>
</template>

<style scoped>
.toast {
  position: fixed;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  color: var(--ink);
}
.toast--danger {
  border-color: var(--fail);
}

/* corner：右下角卡片 */
.toast--corner {
  right: 18px;
  bottom: 18px;
  z-index: 70;
  width: min(340px, calc(100vw - 36px));
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
  padding: 12px 14px;
}

/* bottom：底部居中细条 */
.toast--bottom {
  left: 50%;
  bottom: calc(52px + env(safe-area-inset-bottom));
  z-index: 75;
  display: flex;
  align-items: center;
  gap: 10px;
  max-width: calc(100vw - 24px);
  padding: 8px 12px;
  transform: translateX(-50%);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  font-size: 12px;
}

.body {
  display: block;
  width: 100%;
  min-width: 0;
  text-align: left;
  background: transparent;
  border: none;
  color: var(--ink);
  padding: 0;
  font: inherit;
}
.toast--corner .body {
  padding-right: 18px;
}
.toast--bottom .body {
  width: auto;
  flex: 1;
  overflow-wrap: anywhere;
}
button.body:hover .tx {
  color: var(--accent);
}

.t1 {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--ink-2);
  font-size: 12px;
  line-height: 1.35;
}
.toast--danger .t1 {
  color: var(--fail);
}

.tx {
  display: block;
  color: var(--ink);
  font-size: 13px;
  line-height: 1.45;
}
.toast--corner .tx {
  margin-top: 6px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.toast--bottom .tx {
  font-size: 12px;
}
.toast--bottom.toast--danger .tx {
  color: var(--fail);
}

.meta {
  flex: none;
  color: var(--run);
  font-size: 12px;
}

.act {
  flex: none;
  color: var(--accent);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 2px 8px;
  font-size: 12px;
}

.close {
  position: absolute;
  top: 8px;
  right: 10px;
  z-index: 1;
  background: transparent;
  border: none;
  color: var(--ink-3);
  font-size: 16px;
  line-height: 1;
  padding: 2px;
}
.close:hover {
  color: var(--ink);
}

@media (max-width: 760px) {
  .toast--corner {
    top: 56px;
    right: 12px;
    bottom: auto;
    left: 12px;
    width: auto;
    padding: 6px 26px 6px 10px;
  }
  /* 压成一行：标题 + 正文同一行，正文超长省略；配合自动收起，不长时间挡住会话内容。 */
  .toast--corner .body {
    display: flex;
    align-items: baseline;
    gap: 6px;
    padding-right: 0;
  }
  .toast--corner .t1 {
    flex: none;
    gap: 0;
    font-size: 11px;
  }
  .toast--corner .tx {
    flex: 1;
    min-width: 0;
    margin-top: 0;
    font-size: 11px;
  }
  .toast--corner .close {
    top: 4px;
    right: 6px;
  }
}
</style>
