<script setup lang="ts">
// 侧抽屉外壳（设计稿 §8）：只做外壳，内容走默认插槽。行为与 Sheet 同一套
// （Esc 关闭、遮罩关闭、焦点循环与归还、body 不滚）。
import { ref } from 'vue'
import { t } from '../i18n'
import { useOverlayBehavior } from './overlay'

const open = defineModel<boolean>({ default: false })
withDefaults(defineProps<{ label: string; side?: 'right' | 'left'; width?: string }>(), {
  side: 'right',
  width: '380px',
})

const panel = ref<HTMLElement | null>(null)
const onKeydown = useOverlayBehavior(open, { panel })
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="dw-layer" data-test="drawer" role="presentation" @keydown="onKeydown">
      <div class="dw-scrim" data-test="drawer-scrim" @click="open = false"></div>
      <aside
        ref="panel"
        class="dw-panel"
        :class="[`dw-panel--${side}`]"
        role="dialog"
        aria-modal="true"
        tabindex="-1"
        :aria-label="label"
        :style="{ '--dw-width': width }"
      >
        <header class="dw-head">
          <h2 class="dw-title">{{ label }}</h2>
          <button
            class="dw-close"
            type="button"
            :aria-label="t('act.close')"
            data-test="drawer-close"
            @click="open = false"
          >
            ✕
          </button>
        </header>
        <div class="dw-body">
          <slot />
        </div>
      </aside>
    </div>
  </Teleport>
</template>

<style scoped>
.dw-layer {
  position: fixed;
  inset: 0;
  z-index: 60;
}
.dw-scrim {
  position: absolute;
  inset: 0;
  background: var(--scrim);
}
.dw-panel {
  position: absolute;
  top: 0;
  bottom: 0;
  width: min(var(--dw-width), 92vw);
  display: flex;
  flex-direction: column;
  background: var(--surface);
  box-shadow: var(--shadow-pop);
  outline: none;
}
.dw-panel--right {
  right: 0;
  border-left: 1px solid var(--line-strong);
  border-radius: var(--r-sheet) 0 0 var(--r-sheet);
  animation: dw-in-right var(--dur-slow) var(--ease);
}
.dw-panel--left {
  left: 0;
  border-right: 1px solid var(--line-strong);
  border-radius: 0 var(--r-sheet) var(--r-sheet) 0;
  animation: dw-in-left var(--dur-slow) var(--ease);
}
.dw-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--s2);
  padding: var(--s3) var(--s4);
  border-bottom: 1px solid var(--line);
}
.dw-title {
  margin: 0;
  font-size: var(--fs-lg);
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dw-close {
  flex: none;
  min-width: 40px;
  min-height: 40px;
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--fg-2);
  font: inherit;
  cursor: pointer;
}
.dw-close:hover {
  color: var(--fg);
  border-color: var(--line-strong);
}
.dw-close:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.dw-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--s3) var(--s4);
  -webkit-overflow-scrolling: touch;
}
@keyframes dw-in-right {
  from {
    transform: translateX(24px);
    opacity: 0;
  }
}
@keyframes dw-in-left {
  from {
    transform: translateX(-24px);
    opacity: 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .dw-panel {
    animation: none;
  }
}
/* 手机：侧抽屉铺满宽（两级页面的详情层） */
@media (max-width: 760px) {
  .dw-panel {
    width: 100vw;
  }
}
</style>
