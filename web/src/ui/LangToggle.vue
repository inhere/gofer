<script setup lang="ts">
// 顶栏语言开关（设计稿 §6.1 / §16-2 相邻项）：直接切换 zh / en，不再藏在「更多」菜单里。
import { computed } from 'vue'
import { locale, toggleLocale } from '../store/locale'
import { t } from '../i18n'

// 按钮上写「要切到的语言」：现在是中文就显示 EN。
const next = computed(() => (locale.value === 'zh-CN' ? 'EN' : '中'))
const label = computed(() => (locale.value === 'zh-CN' ? t('util.lang.switchToEn') : t('util.lang.switchToZh')))
</script>

<template>
  <button
    class="lang-toggle"
    type="button"
    :aria-label="label"
    :title="label"
    data-test="lang-toggle"
    @click="toggleLocale"
  >
    {{ next }}
  </button>
</template>

<style scoped>
.lang-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 30px;
  background: transparent;
  color: var(--ink-2);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 4px 7px;
  font-size: 12px;
  font-weight: 600;
  line-height: 1.2;
}
.lang-toggle:hover {
  border-color: var(--accent);
  color: var(--ink);
}
</style>
