<script setup lang="ts">
// 撤销窗口 toast（design §2.3）：「已批准 · 撤销 5s」，按 z 或点按钮撤销；操作失败时显示原因。
import { actionError, undoQueue } from '../../store/today'

const current = undoQueue.current

function clearError(): void {
  actionError.value = ''
}
</script>

<template>
  <div v-if="current || actionError" class="ut mono" role="status" aria-live="polite" data-test="undo-toast">
    <template v-if="current">
      <span class="ut-label">{{ current.label }}</span>
      <span class="ut-cd">{{ current.left }}s</span>
      <button class="ut-btn" type="button" @click="undoQueue.undo()">撤销 z</button>
    </template>
    <template v-else>
      <span class="ut-err">操作失败：{{ actionError }}</span>
      <button class="ut-btn" type="button" @click="clearError">知道了</button>
    </template>
  </div>
</template>

<style scoped>
.ut {
  position: fixed;
  left: 50%;
  bottom: calc(52px + env(safe-area-inset-bottom));
  z-index: 75;
  display: flex;
  align-items: center;
  gap: 10px;
  max-width: calc(100vw - 24px);
  padding: 8px 12px;
  transform: translateX(-50%);
  color: var(--paper);
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  font-size: 12px;
}
.ut-cd {
  color: var(--run);
}
.ut-err {
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--fail);
}
.ut-btn {
  flex: none;
  color: var(--phosphor);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 8px;
  font-size: 12px;
}
</style>
