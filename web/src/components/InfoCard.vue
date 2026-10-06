<script setup lang="ts">
// 公共卡片外壳（Sessions 页三个区 + 「工作」页共用，不复制）。
//  - 默认只显示关键信息：标题 / badges / meta 一两行 / 主要操作按钮；
//  - 其余放 details 插槽，点「详情 ▾」展开（expanded 由父级控制，见 utils/cardExpand）；
//  - 本组件不依赖 router，也不拿业务数据，方便单测（SSR 渲染）。
//  - 末尾的非 scoped <style> 是卡片 / 徽标 / 网格的公共样式（.icard* / .sbadge* / .icard-grid），
//    谁用到 InfoCard 谁就带上，页面里不要再复制一份。
withDefaults(
  defineProps<{
    // 左侧色条：live 进行中 / hot 要你处理 / warn 受阻 / fail 异常 / ok 完成 / idle·off 平静或离线
    tone?: 'live' | 'hot' | 'warn' | 'fail' | 'ok' | 'idle' | 'off' | ''
    expanded?: boolean
    expandable?: boolean
    // 标题可点（打开详情抽屉）
    openable?: boolean
    // 暗淡（已结束 / 离线）：操作区不受影响
    dim?: boolean
    // 当前被打开（抽屉对应的那张）
    active?: boolean
    cardId?: string
  }>(),
  { tone: '', expanded: false, expandable: true, openable: false, dim: false, active: false, cardId: '' },
)

const emit = defineEmits<{
  (e: 'toggle'): void
  (e: 'open'): void
}>()
</script>

<template>
  <article
    class="icard"
    :class="[tone ? `icard--${tone}` : '', { 'icard--dim': dim, 'icard--active': active, 'icard--expanded': expanded }]"
    :data-card-id="cardId || undefined"
  >
    <header class="icard-head">
      <h3
        class="icard-title"
        :class="{ 'icard-title--open': openable }"
        :role="openable ? 'button' : undefined"
        :tabindex="openable ? 0 : undefined"
        data-test="card-title"
        @click="openable && emit('open')"
        @keydown.enter="openable && emit('open')"
      >
        <slot name="title" />
      </h3>
      <div v-if="$slots.badges" class="icard-badges"><slot name="badges" /></div>
    </header>
    <div v-if="$slots.meta" class="icard-meta mono"><slot name="meta" /></div>
    <slot name="body" />
    <div v-if="$slots.actions || (expandable && $slots.details)" class="icard-actions">
      <slot name="actions" />
      <button
        v-if="expandable && $slots.details"
        class="icard-btn icard-toggle mono"
        type="button"
        data-test="card-toggle"
        :aria-expanded="expanded"
        @click.stop="emit('toggle')"
      >{{ expanded ? '收起 ▴' : '详情 ▾' }}</button>
    </div>
    <div v-if="expanded && $slots.details" class="icard-details" data-test="card-details"><slot name="details" /></div>
  </article>
</template>

<style>
/* 卡片公共样式：Sessions 页三个区与「工作」页共用（InfoCard 引入）。
   只用语义 token（--panel / --line / --paper / --queue …），深浅主题自动适配。 */

/* 卡片网格：手机单列，桌面按宽度自动多列 */
.icard-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(330px, 1fr));
  gap: 10px;
  align-items: start;
}
@media (max-width: 640px) {
  .icard-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}

.icard {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  padding: 10px 12px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-left-width: 3px;
  border-radius: var(--radius);
}
.icard--live { border-left-color: var(--phosphor); }
.icard--hot { border-left-color: var(--run); }
.icard--fail { border-left-color: var(--fail); }
.icard--warn { border-left-color: var(--run); }
.icard--ok { border-left-color: var(--done); }
.icard--idle,
.icard--off { border-left-color: var(--queue); }
.icard--active { border-color: var(--phosphor); }
/* 暗淡：已结束 / 离线。操作区保持清晰可点。 */
.icard--dim > .icard-head,
.icard--dim > .icard-meta,
.icard--dim > .icard-details {
  opacity: 0.7;
}

.icard-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
}
.icard-title {
  flex: 1;
  min-width: 0;
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  line-height: 1.4;
  color: var(--paper);
  overflow-wrap: anywhere;
}
.icard-title--open {
  cursor: pointer;
}
.icard-title--open:hover,
.icard-title--open:focus-visible {
  color: var(--phosphor);
  outline: none;
}
.icard-badges {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 4px;
  flex-shrink: 0;
}

.icard-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 10px;
  min-width: 0;
  font-size: 11px;
  color: var(--queue);
}
.icard-meta > * {
  min-width: 0;
}
.icard-line {
  margin: 0;
  font-size: 12px;
  color: var(--paper);
  overflow-wrap: anywhere;
}
.icard-line--muted {
  color: var(--queue);
}
.icard-line--clamp {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.icard-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
}
.icard-actions .icard-toggle {
  margin-left: auto;
}

.icard-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 28px;
  padding: 3px 10px;
  font-size: 11px;
  color: var(--paper);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  text-decoration: none;
  cursor: pointer;
}
.icard-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
  color: var(--phosphor);
  text-decoration: none;
}
.icard-btn:disabled {
  opacity: 0.45;
  cursor: default;
}
.icard-btn--primary {
  color: var(--phosphor);
}
@media (max-width: 640px) {
  .icard-btn {
    min-height: 34px;
    padding: 4px 12px;
    font-size: 12px;
  }
}

.icard-details {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 2px;
  padding-top: 8px;
  border-top: 1px dashed var(--line);
  min-width: 0;
}
.icard-kv {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 4px 10px;
  margin: 0;
  font-size: 11px;
}
.icard-kv dt {
  color: var(--queue);
  white-space: nowrap;
}
.icard-kv dd {
  margin: 0;
  min-width: 0;
  color: var(--paper);
  overflow-wrap: anywhere;
}
.icard-block {
  margin: 0;
  padding: 6px 8px;
  font-size: 11px;
  line-height: 1.5;
  color: var(--paper);
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 180px;
  overflow: auto;
}

/* 状态 / 标签徽标 */
.sbadge {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: 9px;
  padding: 1px 7px;
  font-size: 10px;
  line-height: 1.5;
  color: var(--queue);
  white-space: nowrap;
}
.sbadge--live { color: var(--phosphor); border-color: var(--phosphor); }
.sbadge--hot,
.sbadge--warn { color: var(--run); border-color: var(--run); }
.sbadge--fail { color: var(--fail); border-color: var(--fail); }
.sbadge--ok { color: var(--done); border-color: var(--done); }
.sbadge--idle,
.sbadge--off { color: var(--queue); border-color: var(--queue); }

.icard-chip {
  display: inline-block;
  max-width: 100%;
  padding: 0 6px;
  font-size: 10px;
  line-height: 1.6;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: 3px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
a.icard-chip {
  color: var(--phosphor);
}
</style>
