<script setup lang="ts">
// 顶栏「更多 ▾」：主导航行放不下的导航项收在这个下拉里，分组标题照样显示（navMoreGroups）。
// 面板横着排：一个分组一行，行首是组标题，链接接着横排；宽度随内容，超过视口时换行。
// 选项是链接，不是菜单项——所以用 disclosure 模式（aria-haspopup + aria-expanded），
// 不用 role="menu"（那要求全是 menuitem，会压掉分组标题）。
// 键盘：触发按钮是 <button>，Enter / Space 由浏览器原生派发 click；Esc 关闭并把焦点还给
// 按钮；点面板外关闭；切换路由后自动收起（点链接就走）。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { isNavMoreActive, navMoreGroups } from '../utils/nav'

const route = useRoute()
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)

const active = computed(() => isNavMoreActive(route.path))

function closeMenu(): void {
  open.value = false
  document.removeEventListener('pointerdown', onPointerDown)
  document.removeEventListener('keydown', onKeydown)
}

function openMenu(): void {
  open.value = true
  document.addEventListener('pointerdown', onPointerDown)
  document.addEventListener('keydown', onKeydown)
}

function toggleMenu(): void {
  if (open.value) {
    closeMenu()
  } else {
    openMenu()
  }
}

function onPointerDown(event: PointerEvent): void {
  const target = event.target
  if (target instanceof Node && rootRef.value?.contains(target)) {
    return
  }
  closeMenu()
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Escape') return
  closeMenu()
  triggerRef.value?.focus()
}

watch(
  () => route.path,
  () => closeMenu(),
)

onBeforeUnmount(closeMenu)
</script>

<template>
  <div ref="rootRef" class="nav-more">
    <button
      ref="triggerRef"
      class="nav-more-trigger mono"
      :class="{ 'nav-more-trigger--active': active, 'nav-more-trigger--open': open }"
      type="button"
      aria-haspopup="true"
      :aria-expanded="open"
      data-test="nav-more-btn"
      @click="toggleMenu"
    >
      更多
      <span class="nav-more-caret" aria-hidden="true">▾</span>
    </button>

    <div v-if="open" class="nav-more-panel mono" aria-label="更多导航" data-test="nav-more-panel">
      <div v-for="group in navMoreGroups" :key="group.label" class="nav-more-row" data-test="nav-more-row">
        <p class="nav-more-title">{{ group.label }}</p>
        <div class="nav-more-links">
          <RouterLink
            v-for="item in group.items"
            :key="item.to"
            :to="item.to"
            class="nav-more-item"
            active-class="nav-more-item--active"
          >
            {{ item.label }}
          </RouterLink>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.nav-more {
  position: relative;
  display: inline-flex;
  align-items: center;
}

.nav-more-trigger {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: transparent;
  border: 0;
  color: var(--queue);
  padding: 2px 0;
  font: inherit;
  font-size: 13px;
  line-height: inherit;
  white-space: nowrap;
  border-bottom: 1px solid transparent;
}
.nav-more-trigger:hover,
.nav-more-trigger:focus-visible {
  color: var(--paper);
}
/* 当前页在下拉里时与普通导航项同一高亮口径。 */
.nav-more-trigger--active {
  color: var(--phosphor);
  border-bottom-color: var(--phosphor);
}
.nav-more-trigger--open {
  color: var(--paper);
}
.nav-more-caret {
  font-size: 10px;
  line-height: 1;
}

/* 横向面板：宽度随内容，最宽到视口减两侧边距；比触发按钮宽得多，所以居中挂在按钮下方
   （桌面顶栏只在 >900px 出现，「更多」始终在视口中线左侧，居中后不会越界）。 */
.nav-more-panel {
  position: absolute;
  top: calc(100% + 10px);
  left: 50%;
  transform: translateX(-50%);
  z-index: 40;
  width: max-content;
  max-width: calc(100vw - 28px);
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 7px;
  box-shadow: 0 14px 34px rgba(0, 0, 0, 0.22);
}

/* 一个分组一行：组标题在行首，链接横排在后；一行放不下就换行。 */
.nav-more-row {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 0 8px;
  padding: 2px 0;
}

.nav-more-title {
  flex: none;
  margin: 0 2px 0 2px;
  color: var(--queue);
  font-size: 10px;
  letter-spacing: 0.08em;
  opacity: 0.75;
}

.nav-more-links {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 0 2px;
}

.nav-more-item {
  display: block;
  color: var(--paper);
  border: 1px solid transparent;
  border-radius: calc(var(--radius) - 2px);
  padding: 5px 8px;
  font-size: 12px;
  white-space: nowrap;
}
.nav-more-item:hover,
.nav-more-item:focus-visible {
  background: var(--ink);
  border-color: var(--line);
  text-decoration: none;
}
.nav-more-item--active {
  color: var(--phosphor);
}
</style>
