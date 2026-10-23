<script setup lang="ts">
// 主题切换器：图标表示「将切换到的目标模式」（深色时显示太阳，浅色时显示月亮）。
import { computed } from 'vue'
import { Moon, Sun } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import { toggleTheme, useTheme } from '../store/theme'

const { theme } = useTheme()
const isDark = computed(() => theme.value === 'dark')
const label = computed(() => (isDark.value ? '切换到浅色模式' : '切换到深色模式'))
</script>

<template>
  <button
    class="theme-toggle mono"
    type="button"
    :aria-label="label"
    :title="label"
    @click="toggleTheme"
  >
    <Icon class="theme-glyph" :icon="isDark ? Sun : Moon" size="sm" />
  </button>
</template>

<style scoped>
.theme-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 9px;
  font-size: 13px;
  line-height: 1;
}
.theme-toggle:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.theme-glyph {
  display: block;
}
</style>
