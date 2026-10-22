<script setup lang="ts">
// 对话小地图（信箱时间线右边缘的一条竖向刻度）：每封信一个标记，按在对话里的位置排列，
// 颜色区分收件 / 发件 / 急件 / 未读，当前可视范围有一条高亮带；点标记滚到那封信，悬停
// 显示发件方与时间、首行摘要。位置 / 颜色 / 悬停文案的规则都在 utils/mailMinimap.ts，
// 这里只渲染与派发点击（跳转由页面做，用时间线里每封信的 id）。
import type { MailMinimapMark, MinimapBand } from '../../utils/mailMinimap'

const props = defineProps<{
  marks: MailMinimapMark[]
  band: MinimapBand
}>()
const emit = defineEmits<{ (e: 'jump', mark: MailMinimapMark): void }>()

// props.marks 由页面按需要（封数够多）裁掉，组件不再判断是否显示。
function label(mk: MailMinimapMark): string {
  return `第 ${mk.seq} 封：${mk.tooltip.split('\n')[0]}`
}
</script>

<template>
  <nav class="minimap" aria-label="对话小地图" data-test="mail-minimap">
    <span
      class="minimap-band"
      :style="{ top: `${props.band.topPct}%`, height: `${props.band.heightPct}%` }"
      data-test="mail-minimap-band"
      aria-hidden="true"
    ></span>
    <button
      v-for="mk in props.marks"
      :key="mk.id"
      class="minimap-mark"
      :class="`minimap-mark--${mk.tone}`"
      type="button"
      :style="{ top: `${mk.topPct}%` }"
      :title="mk.tooltip"
      :aria-label="label(mk)"
      :data-test="`mail-minimap-mark-${mk.id}`"
      @click="emit('jump', mk)"
    ></button>
  </nav>
</template>

<style scoped>
.minimap {
  position: relative;
  flex: 0 0 14px;
  width: 14px;
  height: 100%;
  border-left: 1px solid var(--line);
  background: var(--term-bg);
  border-radius: var(--radius);
}
/* 当前可视范围：一条半透明的带，跟着时间线的滚动走。 */
.minimap-band {
  position: absolute;
  left: 0;
  right: 0;
  background: var(--line);
  opacity: 0.45;
  border-radius: var(--radius);
  pointer-events: none;
}
/* 一封信一个标记：横条居中放在它的位置上，颜色见 mailMinimap.ts 的 tone 优先级。 */
.minimap-mark {
  position: absolute;
  left: 2px;
  width: 8px;
  height: 3px;
  margin-top: -1.5px;
  padding: 0;
  border: 0;
  border-radius: 2px;
  background: var(--queue);
  cursor: pointer;
  opacity: 0.85;
}
.minimap-mark:hover,
.minimap-mark:focus-visible {
  opacity: 1;
  transform: scale(1.25);
  outline: none;
}
.minimap-mark--in {
  background: var(--queue);
}
.minimap-mark--out {
  background: var(--run);
}
.minimap-mark--unread {
  background: var(--phosphor);
}
.minimap-mark--urgent {
  background: var(--fail);
}
/* 手机宽度下隐藏：时间线已经占满宽度，右边缘没有放刻度的位置。 */
@media (max-width: 900px) {
  .minimap {
    display: none;
  }
}
@media (prefers-reduced-motion: reduce) {
  .minimap-mark:hover,
  .minimap-mark:focus-visible {
    transform: none;
  }
}
</style>
