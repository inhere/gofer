<script setup lang="ts">
// 统一按钮（设计稿 §4.2 / §8）：三档语义 —— primary 主操作、secondary 次要、ghost 文字、
// 两档尺寸（sm 卡片内、md 表单/页头）。只用语义 token，深浅主题自动适配；≤760 点按目标 ≥40px。
// 之前各页各写一份 `.btn`（工作项页干脆没写，于是渲染成浏览器默认按钮），这里是唯一实现。
import type { ButtonVariant, ButtonSize } from './button'

withDefaults(
  defineProps<{
    variant?: ButtonVariant
    size?: ButtonSize
    type?: 'button' | 'submit'
    disabled?: boolean
    /** 原生 title：灰显原因、补充说明 */
    title?: string
    /** 只有图标 / 缩写时给读屏名称 */
    ariaLabel?: string
    /** 占满整行（手机上的主操作） */
    block?: boolean
  }>(),
  { variant: 'secondary', size: 'sm', type: 'button', disabled: false, title: '', ariaLabel: '', block: false },
)
</script>

<template>
  <button
    :type="type"
    class="ubtn"
    :class="[`ubtn--${variant}`, `ubtn--${size}`, { 'ubtn--block': block }]"
    :disabled="disabled"
    :title="title || undefined"
    :aria-label="ariaLabel || undefined"
  >
    <slot />
  </button>
</template>

<style scoped>
.ubtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--s1);
  min-width: 0;
  font: inherit;
  font-weight: 500;
  line-height: 1.2;
  color: var(--ink);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  white-space: nowrap;
  cursor: pointer;
  transition: background-color 150ms var(--ease), border-color 150ms var(--ease), color 150ms var(--ease);
}
.ubtn--sm {
  min-height: 28px;
  padding: 3px 10px;
  font-size: var(--fs-xs);
}
.ubtn--md {
  min-height: 34px;
  padding: 5px var(--s3);
  font-size: var(--fs-sm);
}
.ubtn--block {
  width: 100%;
}
.ubtn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.ubtn:disabled {
  opacity: 0.45;
  cursor: default;
}
.ubtn--primary {
  color: var(--surface);
  background: var(--accent);
  border-color: var(--accent);
}
.ubtn--primary:hover:not(:disabled) {
  background: var(--accent);
  filter: brightness(1.08);
}
.ubtn--secondary {
  color: var(--ink-2);
  background: var(--surface);
  border-color: var(--line);
}
.ubtn--secondary:hover:not(:disabled) {
  color: var(--ink);
  border-color: var(--accent);
}
.ubtn--ghost {
  color: var(--ink-3);
  background: transparent;
  border-color: transparent;
}
.ubtn--ghost:hover:not(:disabled) {
  color: var(--accent);
  background: var(--accent-soft);
}
/* 手机：点按目标 ≥40px（设计稿 §10）。 */
@media (max-width: 760px) {
  .ubtn--sm,
  .ubtn--md {
    min-height: 40px;
    padding: 4px 12px;
    font-size: var(--fs-sm);
  }
}
</style>
