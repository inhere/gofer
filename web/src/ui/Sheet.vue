<script setup lang="ts">
// 底部抽屉外壳（设计稿 §6.3、§8、§10）：只做外壳，内容由默认插槽提供。
// 行为：Esc 关闭、点遮罩关闭、焦点锁在面板内并在关闭时回到打开者、body 不跟着滚、
// 抓手上滑（swipe down）关闭，手机上按拇指点按目标 ≥40px。
import { ref } from 'vue'
import { t } from '../i18n'
import { useOverlayBehavior } from './overlay'

const open = defineModel<boolean>({ default: false })
const props = withDefaults(defineProps<{ label?: string; sheetId?: string }>(), { label: '', sheetId: '' })

const panel = ref<HTMLElement | null>(null)
const onKeydown = useOverlayBehavior(open, { panel })

const dragStartY = ref<number | null>(null)
function onPointerDown(event: PointerEvent): void {
  dragStartY.value = event.clientY
}
function onPointerUp(event: PointerEvent): void {
  const start = dragStartY.value
  dragStartY.value = null
  // 向下拖超过 60px 视为关闭（抽屉不需要精确手势）
  if (start !== null && event.clientY - start > 60) {
    open.value = false
  }
}

const title = props.label || t('act.filter')
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="ovl-layer"
      data-test="sheet"
      role="presentation"
      @keydown="onKeydown"
    >
      <div class="ovl-scrim" data-test="sheet-scrim" @click="open = false"></div>
      <div
        ref="panel"
        class="ovl-sheet"
        role="dialog"
        aria-modal="true"
        tabindex="-1"
        :aria-label="title"
        :id="sheetId || undefined"
      >
        <div
          class="ovl-handle"
          data-test="sheet-handle"
          aria-hidden="true"
          @pointerdown="onPointerDown"
          @pointerup="onPointerUp"
        >
          <span class="ovl-grip"></span>
        </div>
        <div class="ovl-head">
          <h2 v-if="label" class="ovl-title">{{ label }}</h2>
          <button
            class="ovl-close"
            type="button"
            :aria-label="t('act.close')"
            data-test="sheet-close"
            @click="open = false"
          >
            ✕
          </button>
        </div>
        <div class="ovl-body">
          <slot />
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.ovl-layer {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: flex-end;
  justify-content: center;
}
.ovl-scrim {
  position: absolute;
  inset: 0;
  background: var(--scrim);
}
.ovl-sheet {
  position: relative;
  width: 100%;
  max-width: 560px;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  border-top: 1px solid var(--line-strong);
  border-radius: var(--r-sheet) var(--r-sheet) 0 0;
  box-shadow: var(--shadow-pop);
  outline: none;
  animation: ovl-rise var(--dur-slow) var(--ease);
}
.ovl-handle {
  display: flex;
  justify-content: center;
  padding: var(--s2) 0 var(--s1);
  touch-action: none;
  cursor: grab;
}
.ovl-grip {
  width: 36px;
  height: 4px;
  border-radius: 2px;
  background: var(--line-strong);
}
.ovl-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--s2);
  padding: 0 var(--s4) var(--s2);
}
.ovl-title {
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--fg);
}
.ovl-close {
  margin-left: auto;
  min-width: 40px;
  min-height: 40px;
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--fg-2);
  font: inherit;
  cursor: pointer;
}
.ovl-close:hover {
  color: var(--fg);
  border-color: var(--line-strong);
}
.ovl-close:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.ovl-body {
  padding: 0 var(--s4) var(--s5);
  overflow-y: auto;
}
@keyframes ovl-rise {
  from {
    transform: translateY(16px);
    opacity: 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .ovl-sheet {
    animation: none;
  }
}
/* 桌面也有底部抽屉（筛选、写信这类短表单），但限宽并且离底边留一点空隙 */
@media (min-width: 761px) {
  .ovl-layer {
    align-items: center;
  }
  .ovl-sheet {
    border-radius: var(--r-sheet);
    border: 1px solid var(--line-strong);
    max-height: 80vh;
  }
}
</style>
