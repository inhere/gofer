<script setup lang="ts">
// 设置区外壳（UI 重设计 P5-d，设计稿 §7.4）：二级导航 + <router-view>。
//
// 桌面（≥761）是左侧竖排导航；手机（≤760）收成下拉框（预览稿 .set-nav/.set-select）。
// 加一个设置页 = sections 里加一行 + router.routes.ts 加一个子路由，不用再动侧栏。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { t } from '../../i18n'

interface SettingsSection {
  to: string
  /** 导航文案的 i18n key（settings.nav.*）。 */
  key: string
}

const sections: SettingsSection[] = [
  { to: '/settings/config', key: 'settings.nav.config' },
  // JOB-06①：强制规则库（与「配置管理」同属「资产」，放在它后面）。
  { to: '/settings/rules', key: 'settings.nav.rules' },
  // MCP-06：MCP 注册表，与 Rules / Skills 同属 agent 的「资产」。
  { to: '/settings/mcp', key: 'settings.nav.mcp' },
  { to: '/settings/tunnels', key: 'settings.nav.tunnels' },
  { to: '/settings/notifications', key: 'settings.nav.notifications' },
  // 权限目录 / 授权 / 会话授权 / 审计（根管理员可写，其余只读）。
  { to: '/settings/permissions', key: 'settings.nav.permissions' },
  // W2a：工作项的被动整理 / 请求超时 / 自动交接 / 每日摘要
  { to: '/settings/work', key: 'settings.nav.work' },
  // K5：扩展设置（数据库里的降噪阈值 / 排除名单 / 提示词覆盖）。
  { to: '/settings/extensions', key: 'settings.nav.extensions' },
  { to: '/settings/skills', key: 'settings.nav.skills' },
  { to: '/settings/about', key: 'settings.nav.about' },
]

const route = useRoute()
const router = useRouter()

// 子路径也算命中（/settings/tunnels 下若将来有详情页，菜单项仍高亮）。
function isActive(to: string): boolean {
  return route.path === to || route.path.startsWith(to + '/')
}

const active = computed(() => sections.find((s) => isActive(s.to)))
const activeLabel = computed(() => (active.value ? t(active.value.key) : ''))

// 手机下拉框：选中即跳转（replace 不堆历史，与点导航等价）。
function onPick(event: Event): void {
  const to = (event.target as HTMLSelectElement).value
  if (to && to !== route.path) void router.push(to)
}

// 深链进来时把选中项写回下拉框（route.path 不在表里时不动）。
const selected = computed(() => active.value?.to ?? '')
</script>

<template>
  <div class="settings">
    <div class="settings-head">
      <h1 class="title">{{ t('nav.settings') }}</h1>
      <span v-if="activeLabel" class="crumb">{{ activeLabel }}</span>
    </div>

    <div class="settings-body">
      <nav class="set-nav" :aria-label="t('settings.navLabel')">
        <RouterLink v-for="s in sections" :key="s.to" :to="s.to" class="nav-item">
          {{ t(s.key) }}
        </RouterLink>
      </nav>

      <div class="settings-pane">
        <label class="set-select-wrap">
          <span class="sr-only">{{ t('settings.navLabel') }}</span>
          <select
            class="set-select"
            :value="selected"
            data-testid="settings-nav-select"
            @change="onPick"
          >
            <option v-for="s in sections" :key="s.to" :value="s.to">{{ t(s.key) }}</option>
          </select>
        </label>
        <RouterView />
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings {
  min-width: 0;
}
.settings-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 14px;
}
.title {
  margin: 0;
  font-size: var(--fs-2xl);
  color: var(--ink);
}
.crumb {
  color: var(--accent);
  font-size: var(--fs-sm);
}
.crumb::before {
  content: '▸ ';
  color: var(--ink-3);
}

.settings-body {
  display: grid;
  grid-template-columns: 210px minmax(0, 720px);
  gap: 32px;
  align-items: start;
}
.set-nav {
  display: grid;
  gap: 2px;
  align-content: start;
  position: sticky;
  top: 0;
}
.nav-item {
  text-align: left;
  padding: 7px 10px;
  border-radius: var(--r-ctl);
  color: var(--ink-2);
  font-size: var(--fs-md);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.nav-item:hover {
  background: var(--sunken);
  color: var(--ink);
  text-decoration: none;
}
.nav-item[aria-current='page'] {
  background: var(--surface);
  color: var(--ink);
  font-weight: 600;
  box-shadow: var(--shadow-1), inset 0 0 0 1px var(--line);
}

.settings-pane {
  min-width: 0;
}

/* 手机才有的下拉框；桌面上隐藏（保留在 DOM 里便于读屏器与测试定位）。 */
.set-select-wrap {
  display: none;
}
.set-select {
  width: 100%;
  min-height: 40px;
  padding: 8px 10px;
  color: var(--ink);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-md);
}

/* 平板：导航收窄，保持竖排。 */
@media (max-width: 1099px) {
  .settings-body {
    grid-template-columns: 180px minmax(0, 1fr);
    gap: 24px;
  }
}

/* 手机：竖排导航换成下拉框。 */
@media (max-width: 760px) {
  .settings-body {
    grid-template-columns: minmax(0, 1fr);
    gap: 12px;
  }
  .set-nav {
    display: none;
  }
  .set-select-wrap {
    display: block;
    margin-bottom: 12px;
  }
}
</style>
