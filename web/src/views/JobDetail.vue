<script setup lang="ts">
// 最小占位，T6 实现 Job Detail（日志流 / SSE）
defineProps<{ id: string }>()
</script>

<template>
  <div class="placeholder mono">Job Detail &mdash; T6 <span class="id">({{ id }})</span></div>
</template>

<style scoped>
.placeholder {
  color: var(--queue);
  font-size: 14px;
}
.id {
  color: var(--phosphor);
}
</style>
