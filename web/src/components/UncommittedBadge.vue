<script setup lang="ts">
import { t } from '../i18n'
defineProps<{ count?: number; files?: string[] }>()
</script>

<template>
  <details v-if="count && count > 0" class="uncommitted-badge" @click.stop @keydown.stop>
    <summary class="mono">{{ t('util.badge.uncommitted', { count: count ?? 0 }) }}</summary>
    <div class="uncommitted-popover mono">
      <ul v-if="files && files.length">
        <li v-for="file in files" :key="file">{{ file }}</li>
      </ul>
      <span v-else>{{ t('util.badge.noFiles') }}</span>
      <p v-if="files && count > files.length">{{ t('util.badge.truncated', { shown: files.length, total: count }) }}</p>
    </div>
  </details>
</template>

<style scoped>
.uncommitted-badge { position: relative; display: inline-block; flex: none; font-size: 11px; }
.uncommitted-badge summary { cursor: pointer; list-style: none; padding: 3px 7px; border: 1px solid var(--need); border-radius: var(--r-chip); color: var(--need-ink); white-space: nowrap; }
.uncommitted-badge summary::-webkit-details-marker { display: none; }
.uncommitted-popover { position: absolute; z-index: 20; top: calc(100% + 5px); left: 0; min-width: 240px; max-width: min(420px, 85vw); max-height: 260px; overflow: auto; padding: 8px 10px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--surface); color: var(--ink); box-shadow: 0 8px 24px #0005; }
.uncommitted-popover ul { margin: 0; padding-left: 18px; overflow-wrap: anywhere; }
.uncommitted-popover p { margin: 8px 0 0; opacity: .75; }
</style>
