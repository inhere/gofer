<script setup lang="ts">
// WEB-12：设置区外壳 —— 左侧二级菜单 + <router-view>。
//
// 设置页以后会越来越多（配置管理 / Tunnels / 关于 / …），顶栏只留一个「⚙ 设置」入口，
// 进来后由这里分栏。加一个设置页 = sections 里加一行 + router.ts 加一个子路由，
// 不用再动顶栏。
import { computed } from 'vue'
import { useRoute } from 'vue-router'

interface SettingsSection {
  to: string
  label: string
}

const sections: SettingsSection[] = [
  { to: '/settings/config', label: '配置管理' },
  { to: '/settings/tunnels', label: 'Tunnels' },
  { to: '/settings/about', label: '关于' },
]

const route = useRoute()

// 子路径也算命中（/settings/tunnels 下若将来有详情页，菜单项仍高亮）。
function isActive(to: string): boolean {
  return route.path === to || route.path.startsWith(to + '/')
}

const activeLabel = computed(
  () => sections.find((s) => isActive(s.to))?.label ?? '',
)
</script>

<template>
  <div class="settings">
    <div class="settings-head">
      <span class="eyebrow mono">SETTINGS</span>
      <h1 class="title mono">设置</h1>
      <span v-if="activeLabel" class="crumb mono">{{ activeLabel }}</span>
    </div>

    <div class="settings-body">
      <nav class="settings-nav mono" aria-label="设置分区">
        <RouterLink
          v-for="s in sections"
          :key="s.to"
          :to="s.to"
          class="nav-item"
          :class="{ 'nav-item--active': isActive(s.to) }"
        >
          {{ s.label }}
        </RouterLink>
      </nav>

      <div class="settings-pane">
        <RouterView />
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings {
  max-width: 1180px;
  margin: 0 auto;
}
.settings-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
}
.eyebrow {
  font-size: 10px;
  letter-spacing: 0.18em;
  color: var(--queue);
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.crumb {
  color: var(--phosphor);
  font-size: 12px;
}
.crumb::before {
  content: '▸ ';
  color: var(--queue);
}

.settings-body {
  display: flex;
  align-items: flex-start;
  gap: 18px;
}
.settings-nav {
  flex: none;
  width: 160px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 6px;
  position: sticky;
  top: 12px;
}
.nav-item {
  display: block;
  color: var(--queue);
  border: 1px solid transparent;
  border-radius: var(--radius);
  padding: 7px 10px;
  font-size: 12px;
  white-space: nowrap;
}
.nav-item:hover {
  color: var(--paper);
  background: var(--panel);
  text-decoration: none;
}
.nav-item--active {
  color: var(--phosphor);
  border-color: var(--line);
  background: var(--panel);
}

.settings-pane {
  flex: 1;
  min-width: 0;
}

/* 窄屏：二级菜单折叠为顶部横向 tab。 */
@media (max-width: 768px) {
  .settings-body {
    flex-direction: column;
    gap: 12px;
  }
  .settings-nav {
    width: 100%;
    flex-direction: row;
    position: static;
    overflow-x: auto;
  }
  .nav-item {
    flex: none;
  }
}
</style>
