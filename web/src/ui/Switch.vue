<script setup lang="ts">
// Switch：统一的开关（设计稿「新组件 → 吸收的旧实现」：设置页各自的开关写法）。
//  - v-model 绑定布尔值；role="switch" + aria-checked；按钮原生支持 Space / Enter 切换；
//  - 默认插槽是开关旁的文字（点文字也能切换）；没有可见文字时用 label 给出无障碍名称；
//  - disabled 时不响应点击与键盘，样式变淡。
// 颜色全部走 tokens.css 变量，深浅主题通用。
const props = withDefaults(
  defineProps<{
    modelValue: boolean
    disabled?: boolean
    // 无障碍名称：没有可见文字（默认插槽为空）时必须给
    label?: string
  }>(),
  { disabled: false, label: undefined },
)
const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()

function flip(): void {
  if (props.disabled) return
  emit('update:modelValue', !props.modelValue)
}
</script>

<template>
  <button
    class="sw"
    :class="{ 'sw--on': modelValue }"
    type="button"
    role="switch"
    :aria-checked="modelValue ? 'true' : 'false'"
    :aria-label="label"
    :disabled="disabled"
    data-testid="switch"
    @click="flip"
  >
    <span class="track" aria-hidden="true"><span class="thumb"></span></span>
    <span v-if="$slots.default" class="text"><slot /></span>
  </button>
</template>

<style scoped>
.sw {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 0;
  background: transparent;
  border: none;
  color: var(--ink);
  font: inherit;
  font-size: var(--fs-sm);
  cursor: pointer;
  text-align: left;
}
.sw:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
.sw:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
  border-radius: 999px;
}

.track {
  position: relative;
  flex: none;
  width: 32px;
  height: 18px;
  border-radius: 999px;
  background: var(--sunken);
  border: 1px solid var(--line);
  transition: background var(--dur-fast) var(--ease), border-color var(--dur-fast) var(--ease);
}
.thumb {
  position: absolute;
  top: 1px;
  left: 1px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--ink-3);
  transition: transform var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease);
}
.sw--on .track {
  background: var(--accent-soft);
  border-color: var(--accent);
}
.sw--on .thumb {
  transform: translateX(14px);
  background: var(--accent);
}

@media (prefers-reduced-motion: reduce) {
  .track,
  .thumb {
    transition: none;
  }
}
</style>
