<script setup lang="ts">
// 「实验性」标注：挂在 docs/guide/stability.md 列为「标为实验性 / 默认关闭」的功能入口上，
// 只提示"这块还在变"，不拦任何操作。外观沿用控制台已有的小徽标（Agents 页的「内置」）：
// 10px mono 字、发丝边框、4px 圆角；颜色一律走 styles/tokens.css 的语义变量。
withDefaults(defineProps<{ label?: string; hint?: string }>(), {
  label: '实验性',
  hint: '此功能仍在调整，行为与配置可能变化',
})
</script>

<template>
  <span class="experimental-badge mono" :title="hint" data-test="experimental-badge">{{ label }}</span>
</template>

<style scoped>
/* 提醒色（--run）+ 虚线边框：与状态色区分开，读起来像"还在搭"而不是"出错了"。
   文字不进 tab 序、不响应点击，紧跟在标题 / 开关后面当注脚。 */
.experimental-badge {
  display: inline-flex;
  align-items: center;
  flex: none;
  margin-left: 6px;
  padding: 0 5px;
  font-size: 10px;
  line-height: 16px;
  letter-spacing: 0.06em;
  vertical-align: middle;
  white-space: nowrap;
  color: var(--run);
  border: 1px dashed var(--run);
  border-radius: var(--r-chip);
  cursor: help;
  user-select: none;
}

/* 手机档（与 utils/mobileLayout.ts 的 MOBILE_MAX_WIDTH = 900 同一断点体系，这里取更窄的
   640 只压内边距）：标题行已经会折行，徽标保持一行、不挤占标题的宽度。 */
@media (max-width: 640px) {
  .experimental-badge {
    margin-left: 4px;
    padding: 0 4px;
  }
}
</style>
