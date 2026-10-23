<script setup lang="ts">
// 通用提示条（T4 参数化）：标题/正文/跳转目标由调用方给出。
// to 为空时点击仅关闭（如全局 decision 无 plan 可跳）。
// 手机档（≤900px）提示条压在会话内容上，几秒后自己收掉（停留时间见 utils/mobileLayout）；
// 宽屏不自动收，沿用「等用户点关闭」的行为。
import { onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { isMobileWidth, toastDwellMs } from '../utils/mobileLayout'

const props = defineProps<{ title: string; text: string; to?: string }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'goto'): void
}>()

const router = useRouter()

let dwellTimer: number | null = null

function clearDwell(): void {
  if (dwellTimer != null) {
    window.clearTimeout(dwellTimer)
    dwellTimer = null
  }
}

// 每次弹新的提示都要重新计时（同一条提示的正文变了也算新的）。
function scheduleDwell(): void {
  clearDwell()
  const mobile = typeof window !== 'undefined' && typeof window.matchMedia === 'function'
    ? isMobileWidth(window.innerWidth)
    : false
  const ms = toastDwellMs(mobile)
  if (ms <= 0) return
  dwellTimer = window.setTimeout(() => {
    dwellTimer = null
    emit('close')
  }, ms)
}

onMounted(scheduleDwell)
onUnmounted(clearDwell)
watch(() => [props.title, props.text], scheduleDwell)

function goto(): void {
  emit('goto')
  if (props.to) {
    void router.push(props.to)
  }
}
</script>

<template>
  <div class="toast" role="status" aria-live="polite">
    <button class="close" type="button" aria-label="关闭提示" @click="emit('close')">
      ×
    </button>
    <button class="body" type="button" @click="goto">
      <span class="t1 mono">{{ title }}</span>
      <span class="tx mono">{{ text }}</span>
    </button>
  </div>
</template>

<style scoped>
.toast {
  position: fixed;
  right: 18px;
  bottom: 18px;
  z-index: 70;
  width: min(340px, calc(100vw - 36px));
  background: var(--surface);
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.5);
  padding: 12px 14px;
}

.body {
  display: block;
  width: 100%;
  min-width: 0;
  text-align: left;
  background: transparent;
  border: none;
  color: var(--ink);
  padding: 0 18px 0 0;
}

.body:hover .tx {
  color: var(--accent);
}

.t1 {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--fail);
  font-size: 12px;
  line-height: 1.35;
}

.tx {
  display: block;
  margin-top: 6px;
  color: var(--ink);
  font-size: 13px;
  line-height: 1.45;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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

@media (max-width: 640px) {
  .toast {
    top: 56px;
    right: 12px;
    bottom: auto;
    left: 12px;
    width: auto;
    padding: 6px 26px 6px 10px;
  }
  /* 压成一行：标题 + 正文同一行，正文超长省略；配合 5s 自动收起，不长时间挡住会话内容。 */
  .body {
    display: flex;
    align-items: baseline;
    gap: 6px;
  }
  .t1 {
    flex: none;
    gap: 0;
    font-size: 11px;
  }
  .tx {
    flex: 1;
    min-width: 0;
    margin-top: 0;
    font-size: 11px;
  }
  .close {
    top: 4px;
    right: 6px;
  }
}
</style>
