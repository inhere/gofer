<script setup lang="ts">
defineProps<{ count?: number; files?: string[] }>()
</script>

<template>
  <details v-if="count && count > 0" class="uncommitted-badge" @click.stop @keydown.stop>
    <summary class="mono">未提交 {{ count }}</summary>
    <div class="uncommitted-popover mono">
      <ul v-if="files && files.length">
        <li v-for="file in files" :key="file">{{ file }}</li>
      </ul>
      <span v-else>文件列表不可用</span>
      <p v-if="files && count > files.length">仅显示前 {{ files.length }} 个，共 {{ count }} 个。</p>
    </div>
  </details>
</template>

<style scoped>
.uncommitted-badge { position: relative; display: inline-block; flex: none; font-size: 11px; }
.uncommitted-badge summary { cursor: pointer; list-style: none; padding: 3px 7px; border: 1px solid var(--warn, #c98a35); border-radius: var(--radius); color: var(--warn, #c98a35); white-space: nowrap; }
.uncommitted-badge summary::-webkit-details-marker { display: none; }
.uncommitted-popover { position: absolute; z-index: 20; top: calc(100% + 5px); left: 0; min-width: 240px; max-width: min(420px, 85vw); max-height: 260px; overflow: auto; padding: 8px 10px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--panel); color: var(--paper); box-shadow: 0 8px 24px #0005; }
.uncommitted-popover ul { margin: 0; padding-left: 18px; overflow-wrap: anywhere; }
.uncommitted-popover p { margin: 8px 0 0; opacity: .75; }
</style>
