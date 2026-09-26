<script setup lang="ts">
import { onUnmounted, ref } from 'vue'
import type { LayoutNode } from './layoutTree'
import ThreadView from './ThreadView.vue'

defineOptions({ name: 'LayoutPane' })

const props = defineProps<{
  node: LayoutNode
  path: string
  focusedPath: string
}>()

const emit = defineEmits<{
  focus: [path: string]
  ratio: [path: string, ratio: number]
}>()

const splitRoot = ref<HTMLElement | null>(null)
let stopPointerTracking: (() => void) | null = null

function childPath(side: 'a' | 'b'): string {
  return props.path === '' ? side : `${props.path}.${side}`
}

function startResize(event: PointerEvent): void {
  if (props.node.kind !== 'split' || !splitRoot.value) return
  event.preventDefault()
  event.stopPropagation()
  const rect = splitRoot.value.getBoundingClientRect()
  const dir = props.node.dir
  const onMove = (move: PointerEvent): void => {
    const raw = dir === 'h'
      ? (move.clientX - rect.left) / Math.max(1, rect.width)
      : (move.clientY - rect.top) / Math.max(1, rect.height)
    emit('ratio', props.path, raw)
  }
  const stop = (): void => {
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', stop)
    window.removeEventListener('pointercancel', stop)
    stopPointerTracking = null
  }
  stopPointerTracking?.()
  stopPointerTracking = stop
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', stop)
  window.addEventListener('pointercancel', stop)
}

onUnmounted(() => stopPointerTracking?.())
</script>

<template>
  <section
    v-if="node.kind === 'pane'"
    class="layout-pane"
    :class="{ focused: focusedPath === path }"
    :data-pane-path="path"
    @pointerdown.capture="emit('focus', path)"
  >
    <ThreadView
      v-if="node.thread_id"
      :key="node.thread_id"
      :thread-id="node.thread_id"
      :focused="focusedPath === path"
    />
    <button v-else class="empty-pane mono" type="button" @click="emit('focus', path)">
      空窗格<br /><small>从左侧选择一个会话</small>
    </button>
  </section>
  <section
    v-else
    ref="splitRoot"
    class="layout-split"
    :class="node.dir === 'h' ? 'split--h' : 'split--v'"
  >
    <div class="split-branch" :style="{ flexGrow: node.ratio }">
      <LayoutPane
        :node="node.a"
        :path="childPath('a')"
        :focused-path="focusedPath"
        @focus="emit('focus', $event)"
        @ratio="(path, ratio) => emit('ratio', path, ratio)"
      />
    </div>
    <button
      class="splitter"
      type="button"
      :aria-label="node.dir === 'h' ? '调整左右分屏比例' : '调整上下分屏比例'"
      @pointerdown="startResize"
    ></button>
    <div class="split-branch" :style="{ flexGrow: 1 - node.ratio }">
      <LayoutPane
        :node="node.b"
        :path="childPath('b')"
        :focused-path="focusedPath"
        @focus="emit('focus', $event)"
        @ratio="(path, ratio) => emit('ratio', path, ratio)"
      />
    </div>
  </section>
</template>

<style scoped>
.layout-pane, .layout-split, .split-branch { min-width: 0; min-height: 0; width: 100%; height: 100%; }
.layout-pane { position: relative; overflow: hidden; border: 1px solid transparent; background: var(--ink); }
.layout-pane.focused { border-color: var(--phosphor); }
.layout-split { display: flex; }
.split--h { flex-direction: row; }
.split--v { flex-direction: column; }
.split-branch { flex-basis: 0; overflow: hidden; }
.splitter { flex: none; position: relative; z-index: 3; padding: 0; border: 0; background: var(--line); }
.splitter:hover, .splitter:focus-visible { background: var(--phosphor); outline: none; }
.split--h > .splitter { width: 5px; cursor: col-resize; }
.split--v > .splitter { height: 5px; cursor: row-resize; }
.empty-pane { width: 100%; height: 100%; border: 0; color: var(--queue); background: rgba(255,255,255,.015); }
.empty-pane:hover { color: var(--paper); background: rgba(255,255,255,.035); }
.empty-pane small { font-size: 10px; }
</style>
