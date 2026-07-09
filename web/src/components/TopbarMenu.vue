<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { toggleTheme, useTheme } from '../store/theme'

const emit = defineEmits<{
  logout: []
}>()

const { theme } = useTheme()
const open = ref(false)
const menuRef = ref<HTMLElement | null>(null)
const isDark = computed(() => theme.value === 'dark')
const themeLabel = computed(() => (isDark.value ? '切换到浅色模式' : '切换到深色模式'))

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
  if (target instanceof Node && menuRef.value?.contains(target)) {
    return
  }
  closeMenu()
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    closeMenu()
  }
}

function onThemeClick(): void {
  toggleTheme()
  closeMenu()
}

function onLogoutClick(): void {
  closeMenu()
  emit('logout')
}

onBeforeUnmount(closeMenu)
</script>

<template>
  <div ref="menuRef" class="topbar-menu">
    <button
      class="menu-trigger mono"
      type="button"
      aria-label="打开设置菜单"
      aria-haspopup="menu"
      :aria-expanded="open"
      @click="toggleMenu"
    >
      <span aria-hidden="true">⚙</span>
    </button>

    <div v-if="open" class="menu-panel mono" role="menu">
      <button class="menu-item" type="button" role="menuitem" @click="onThemeClick">
        <span class="menu-icon" aria-hidden="true">{{ isDark ? '☀' : '☾' }}</span>
        <span>{{ themeLabel }}</span>
      </button>
      <button class="menu-item" type="button" role="menuitem" disabled>
        <span class="menu-icon" aria-hidden="true">中</span>
        <span>中文 / EN</span>
      </button>
      <button class="menu-item menu-item--danger" type="button" role="menuitem" @click="onLogoutClick">
        <span class="menu-icon" aria-hidden="true">↪</span>
        <span>登出</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.topbar-menu {
  position: relative;
  display: inline-flex;
}

.menu-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 9px;
  font-size: 13px;
  line-height: 1;
}
.menu-trigger:hover,
.menu-trigger:focus-visible {
  border-color: var(--phosphor);
  color: var(--phosphor);
  outline: none;
}

.menu-panel {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  z-index: 40;
  width: 172px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px;
  box-shadow: 0 14px 34px rgba(0, 0, 0, 0.22);
}

.menu-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  background: transparent;
  color: var(--paper);
  border: 1px solid transparent;
  border-radius: calc(var(--radius) - 2px);
  padding: 7px 8px;
  font-size: 12px;
  text-align: left;
}
.menu-item:hover:not(:disabled),
.menu-item:focus-visible {
  background: var(--ink);
  border-color: var(--line);
  outline: none;
}
.menu-item:disabled {
  color: var(--queue);
  cursor: default;
  opacity: 0.72;
}
.menu-item--danger:hover,
.menu-item--danger:focus-visible {
  color: var(--fail);
}

.menu-icon {
  width: 16px;
  text-align: center;
  flex: none;
}
</style>
