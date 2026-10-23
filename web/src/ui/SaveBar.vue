<script setup lang="ts">
// SaveBar：设置页的保存条（设计稿 §7.4）：有未保存修改时吸附在底部，改完退回普通位置。
// 只画条和两个按钮，保存/放弃由调用方处理；宽屏靠右，手机上两个按钮铺满。
import { computed } from 'vue'
import { t } from '../i18n'

const props = withDefaults(
  defineProps<{
    /** 有未保存修改吗。false 时按钮禁用、提示文字隐藏。 */
    dirty: boolean
    /** 保存中（按钮禁用并换文案）。 */
    busy?: boolean
    /** 保存按钮文案（默认「保存」）。 */
    saveText?: string
    /** 未保存提示的文案（默认「有未保存的修改」）。 */
    unsavedText?: string
  }>(),
  { busy: false, saveText: '', unsavedText: '' },
)

const emit = defineEmits<{ save: []; discard: [] }>()

const saveLabel = computed(() => props.saveText || t('act.save'))
const unsaved = computed(() => props.unsavedText || t('settings.unsaved'))
</script>

<template>
  <div class="save-bar" :class="{ 'save-bar--idle': !dirty }" data-testid="save-bar">
    <span v-if="dirty" class="muted" data-testid="save-bar-unsaved">{{ unsaved }}</span>
    <button
      class="btn btn--ghost"
      type="button"
      :disabled="busy || !dirty"
      data-testid="save-bar-discard"
      @click="emit('discard')"
    >
      {{ t('settings.discard') }}
    </button>
    <button
      class="btn btn--primary"
      type="button"
      :disabled="busy || !dirty"
      data-testid="save-bar-save"
      @click="emit('save')"
    >
      {{ busy ? t('settings.saving') : saveLabel }}
    </button>
  </div>
</template>

<style scoped>
.save-bar {
  position: sticky;
  bottom: 0;
  margin-top: 8px;
  padding: 12px 0;
  display: flex;
  gap: 10px;
  justify-content: flex-end;
  align-items: center;
  background: var(--bg);
  border-top: 1px solid var(--line);
  z-index: 2;
}
/* 没有未保存修改时停在文末，不吸附（避免常驻挡内容）。 */
.save-bar--idle {
  position: static;
}
.save-bar .muted {
  margin-right: auto;
  font-size: var(--fs-sm);
  color: var(--fg-3);
}
.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 7px 14px;
  font: inherit;
  font-size: var(--fs-sm);
  background: transparent;
  color: var(--fg);
}
.btn:hover:not(:disabled) {
  border-color: var(--line-strong);
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn--primary:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

@media (max-width: 760px) {
  .save-bar .btn {
    flex: 1;
  }
  .save-bar .muted {
    display: none;
  }
}
</style>
