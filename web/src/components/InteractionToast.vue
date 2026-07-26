<script setup lang="ts">
// 通用提示条（T4 参数化）：标题/正文/跳转目标由调用方给出。
// to 为空时点击仅关闭（如全局 decision 无 plan 可跳）。
import { useRouter } from 'vue-router'

const props = defineProps<{ title: string; text: string; to?: string }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'goto'): void
}>()

const router = useRouter()

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
  background: var(--panel);
  border: 1px solid var(--fail);
  border-radius: var(--radius);
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
  color: var(--paper);
  padding: 0 18px 0 0;
}

.body:hover .tx {
  color: var(--phosphor);
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
  color: var(--paper);
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
  color: var(--queue);
  font-size: 16px;
  line-height: 1;
  padding: 2px;
}

.close:hover {
  color: var(--paper);
}
</style>
