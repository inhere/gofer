<script setup lang="ts">
// 双栏终端式日志带（stdout | stderr），等宽 mono、深底。
//  - 自动滚底：有新内容滚到底；
//  - 用户上滚 -> 暂停自动滚动 + 显示「N 行新」提示，点击/回到底部恢复；
//  - prefers-reduced-motion -> 关闭平滑滚动惯性（即时 scrollTop）。
import { nextTick, onMounted, ref, watch } from 'vue'

const props = defineProps<{
  stdout: string
  stderr: string
  // 是否运行中：底部 live 脉冲
  live?: boolean
}>()

const smoothScroll = !window.matchMedia('(prefers-reduced-motion: reduce)').matches

const outEl = ref<HTMLElement | null>(null)
const errEl = ref<HTMLElement | null>(null)
const outPinned = ref(true)
const errPinned = ref(true)
const outNew = ref(0)
const errNew = ref(0)
let outPrev = 0
let errPrev = 0

function lineCount(text: string): number {
  if (!text) {
    return 0
  }
  const t = text.endsWith('\n') ? text.slice(0, -1) : text
  return t.length === 0 ? 0 : t.split('\n').length
}

function scrollPane(el: HTMLElement): void {
  if (smoothScroll && typeof el.scrollTo === 'function') {
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  } else {
    el.scrollTop = el.scrollHeight
  }
}

// 是否贴近底部（容差 24px）
function atBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < 24
}

function jumpOut(): void {
  if (outEl.value) {
    scrollPane(outEl.value)
  }
  outPinned.value = true
  outNew.value = 0
}
function jumpErr(): void {
  if (errEl.value) {
    scrollPane(errEl.value)
  }
  errPinned.value = true
  errNew.value = 0
}

function onScrollOut(): void {
  const el = outEl.value
  if (!el) {
    return
  }
  if (atBottom(el)) {
    outPinned.value = true
    outNew.value = 0
  } else {
    outPinned.value = false
  }
}
function onScrollErr(): void {
  const el = errEl.value
  if (!el) {
    return
  }
  if (atBottom(el)) {
    errPinned.value = true
    errNew.value = 0
  } else {
    errPinned.value = false
  }
}

watch(
  () => props.stdout,
  (v) => {
    const total = lineCount(v)
    const delta = Math.max(0, total - outPrev)
    outPrev = total
    void nextTick(() => {
      if (outPinned.value && outEl.value) {
        scrollPane(outEl.value)
        outNew.value = 0
      } else if (delta > 0) {
        outNew.value += delta
      }
    })
  },
)
watch(
  () => props.stderr,
  (v) => {
    const total = lineCount(v)
    const delta = Math.max(0, total - errPrev)
    errPrev = total
    void nextTick(() => {
      if (errPinned.value && errEl.value) {
        scrollPane(errEl.value)
        errNew.value = 0
      } else if (delta > 0) {
        errNew.value += delta
      }
    })
  },
)

onMounted(() => {
  outPrev = lineCount(props.stdout)
  errPrev = lineCount(props.stderr)
  void nextTick(() => {
    if (outEl.value) {
      scrollPane(outEl.value)
    }
    if (errEl.value) {
      scrollPane(errEl.value)
    }
  })
})
</script>

<template>
  <div class="tape">
    <div class="pane">
      <div class="pane-head mono">
        <span class="pane-title">stdout</span>
        <span v-if="live" class="live-pulse" title="streaming">live</span>
      </div>
      <div ref="outEl" class="pane-body mono" @scroll="onScrollOut">
        <pre class="log-text">{{ stdout || '（无 stdout 输出）' }}</pre>
      </div>
      <button
        v-if="outNew > 0 && !outPinned"
        class="new-jump mono"
        type="button"
        @click="jumpOut"
      >
        {{ outNew }} 行新 ↓
      </button>
    </div>

    <div class="pane pane--err">
      <div class="pane-head mono">
        <span class="pane-title">stderr</span>
        <span v-if="live" class="live-pulse" title="streaming">live</span>
      </div>
      <div ref="errEl" class="pane-body mono" @scroll="onScrollErr">
        <pre class="log-text">{{ stderr || '（无 stderr 输出）' }}</pre>
      </div>
      <button
        v-if="errNew > 0 && !errPinned"
        class="new-jump mono"
        type="button"
        @click="jumpErr"
      >
        {{ errNew }} 行新 ↓
      </button>
    </div>
  </div>
</template>

<style scoped>
.tape {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  min-width: 0;
}
.pane {
  position: relative;
  display: flex;
  flex-direction: column;
  min-width: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: #08121a;
  overflow: hidden;
}
.pane-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.08em;
}
.pane-title {
  color: var(--queue);
  text-transform: uppercase;
}
.pane--err .pane-title {
  color: var(--fail);
}
.live-pulse {
  color: var(--run);
  font-size: 10px;
  animation: live-blink 1.2s ease-in-out infinite;
}
@keyframes live-blink {
  0%,
  100% {
    opacity: 0.35;
  }
  50% {
    opacity: 1;
  }
}
.pane-body {
  flex: 1;
  overflow: auto;
  padding: 10px;
  height: 52vh;
  max-height: 52vh;
}
.log-text {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.45;
  color: var(--paper);
}
.new-jump {
  position: absolute;
  bottom: 12px;
  right: 12px;
  background: var(--run);
  color: var(--ink);
  border: none;
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  font-weight: 600;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.4);
}
.new-jump:hover {
  filter: brightness(1.08);
}
@media (prefers-reduced-motion: reduce) {
  .live-pulse {
    animation: none;
    opacity: 0.9;
  }
}
</style>
