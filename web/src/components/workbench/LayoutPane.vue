<script setup lang="ts">
import { onUnmounted, ref } from 'vue'
import { WORKBENCH_THREAD_DRAG_TYPE, type LayoutNode } from './layoutTree'
import ThreadView from './ThreadView.vue'

defineOptions({ name: 'LayoutPane' })

const props = defineProps<{
  node: LayoutNode
  path: string
  focusedPath: string
  maximizedPath?: string
}>()

const emit = defineEmits<{
  focus: [path: string]
  ratio: [path: string, ratio: number]
  dropThread: [path: string, threadId: string, edge: DropEdge]
}>()

type DropEdge = 'center' | 'left' | 'right' | 'top' | 'bottom'

const splitRoot = ref<HTMLElement | null>(null)
const dropZone = ref<DropEdge | ''>('')
let stopPointerTracking: (() => void) | null = null

function childPath(side: 'a' | 'b'): string {
  return props.path === '' ? side : `${props.path}.${side}`
}

function branchContains(branch: string, target: string): boolean {
  return target === branch || target.startsWith(`${branch}.`)
}

function dropEdge(event: DragEvent): DropEdge {
  const element = event.currentTarget as HTMLElement
  const rect = element.getBoundingClientRect()
  const x = (event.clientX - rect.left) / Math.max(1, rect.width)
  const y = (event.clientY - rect.top) / Math.max(1, rect.height)
  const edges: Array<{ edge: Exclude<DropEdge, 'center'>; distance: number }> = [
    { edge: 'left', distance: x },
    { edge: 'right', distance: 1 - x },
    { edge: 'top', distance: y },
    { edge: 'bottom', distance: 1 - y },
  ]
  edges.sort((a, b) => a.distance - b.distance)
  return edges[0].distance <= 0.25 ? edges[0].edge : 'center'
}

function onDragOver(event: DragEvent): void {
  if (!event.dataTransfer?.types.includes(WORKBENCH_THREAD_DRAG_TYPE)) return
  event.preventDefault()
  event.dataTransfer.dropEffect = 'copy'
  dropZone.value = dropEdge(event)
}

function onDragLeave(event: DragEvent): void {
  const current = event.currentTarget as HTMLElement
  if (event.relatedTarget instanceof Node && current.contains(event.relatedTarget)) return
  dropZone.value = ''
}

function onDrop(event: DragEvent): void {
  const threadId = event.dataTransfer?.getData(WORKBENCH_THREAD_DRAG_TYPE) ?? ''
  if (!threadId) return
  event.preventDefault()
  const edge = dropEdge(event)
  dropZone.value = ''
  emit('dropThread', props.path, threadId, edge)
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
    :class="[
      { focused: focusedPath === path },
      dropZone ? `drop--${dropZone}` : '',
    ]"
    :data-pane-path="path"
    @pointerdown.capture="emit('focus', path)"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
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
    <div
      class="split-branch"
      :class="{ 'branch--hidden': maximizedPath && !branchContains(childPath('a'), maximizedPath) }"
      :style="{ flexGrow: node.ratio }"
    >
      <LayoutPane
        :node="node.a"
        :path="childPath('a')"
        :focused-path="focusedPath"
        :maximized-path="maximizedPath"
        @focus="emit('focus', $event)"
        @ratio="(path, ratio) => emit('ratio', path, ratio)"
        @drop-thread="(path, threadId, edge) => emit('dropThread', path, threadId, edge)"
      />
    </div>
    <button
      class="splitter"
      v-show="!maximizedPath"
      type="button"
      :aria-label="node.dir === 'h' ? '调整左右分屏比例' : '调整上下分屏比例'"
      @pointerdown="startResize"
    ></button>
    <div
      class="split-branch"
      :class="{ 'branch--hidden': maximizedPath && !branchContains(childPath('b'), maximizedPath) }"
      :style="{ flexGrow: 1 - node.ratio }"
    >
      <LayoutPane
        :node="node.b"
        :path="childPath('b')"
        :focused-path="focusedPath"
        :maximized-path="maximizedPath"
        @focus="emit('focus', $event)"
        @ratio="(path, ratio) => emit('ratio', path, ratio)"
        @drop-thread="(path, threadId, edge) => emit('dropThread', path, threadId, edge)"
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
.split-branch.branch--hidden { display: none; }
.split-branch.branch--hidden ~ .split-branch { flex-grow: 1 !important; }
.splitter { flex: none; position: relative; z-index: 3; padding: 0; border: 0; background: var(--line); }
.splitter:hover, .splitter:focus-visible { background: var(--phosphor); outline: none; }
.split--h > .splitter { width: 5px; cursor: col-resize; }
.split--v > .splitter { height: 5px; cursor: row-resize; }
.empty-pane { width: 100%; height: 100%; border: 0; color: var(--queue); background: rgba(255,255,255,.015); }
.empty-pane:hover { color: var(--paper); background: rgba(255,255,255,.035); }
.empty-pane small { font-size: 10px; }
.layout-pane.drop--center { box-shadow: inset 0 0 0 3px var(--phosphor); }
.layout-pane.drop--left { box-shadow: inset 18px 0 0 rgba(79,176,198,.35); }
.layout-pane.drop--right { box-shadow: inset -18px 0 0 rgba(79,176,198,.35); }
.layout-pane.drop--top { box-shadow: inset 0 18px 0 rgba(79,176,198,.35); }
.layout-pane.drop--bottom { box-shadow: inset 0 -18px 0 rgba(79,176,198,.35); }
</style>
