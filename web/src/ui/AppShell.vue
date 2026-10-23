<script setup lang="ts">
// 应用外壳（设计稿 §6、§16）：侧栏 / 图标栏、顶栏、信号带、手机标签栏与「更多」抽屉。
//
// 形态完全由视口宽度决定（utils/shell.ts 的 shellForm），不做路由级判断：
//   ≥1100 完整侧栏（可整体收起成图标栏）· 761–1099 图标栏 · ≤760 底部 5 标签 + 更多抽屉。
// 侧栏收起与节点区展开记在 localStorage（readShellPrefs / writeShellPrefs）。
// 页面内容由默认插槽给出，本组件只管外壳。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { clearToken, hasToken } from '../store/auth'
import MailButton from '../components/MailButton.vue'
import QuickCapture from '../components/QuickCapture.vue'
import ThemeToggle from '../components/ThemeToggle.vue'
import NeedsMeButton from './NeedsMeButton.vue'
import CommandPalette from './CommandPalette.vue'
import Wire from './Wire.vue'
import LangToggle from './LangToggle.vue'
import Icon from './Icon.vue'
import { navIcon } from './icon'
import { PanelLeftClose, PanelLeftOpen, Plus, Search, Sparkles } from '@lucide/vue'
import { listRunners } from '../api/client'
import { liveStatus } from '../api/live'
import { decisionCount } from '../store/today'
import { createPoller } from '../utils/poller'
import { t } from '../i18n'
import { staleBuild } from '../store/staleBuild'
import { updateLatest } from '../store/updateAvailable'
import { useWhoami } from '../store/whoami'
import { CEILING_READ_ONLY_NOTICE } from '../utils/ceiling'
import { shouldShowWorkBadge, workBadgeLabel, workNeedsMeCount } from '../store/workNeedsMe'
import {
  DEFAULT_SHELL_PREFS,
  TAB_BAR_ITEMS,
  hasSidebar,
  hasTabBar,
  moreActive,
  moreItems,
  navItemActive,
  readShellPrefs,
  shellForm,
  sidebarExpanded,
  sidebarNavGroups,
  tabBarActive,
  writeShellPrefs,
  type ShellPrefs,
} from '../utils/shell'

const route = useRoute()
const router = useRouter()

const prefs = ref<ShellPrefs>({ ...DEFAULT_SHELL_PREFS })
const viewportW = ref(typeof window === 'undefined' ? 0 : window.innerWidth)
const moreOpen = ref(false)
const paletteOpen = ref(false)
const readOnly = useWhoami().readOnly

const form = computed(() => shellForm(viewportW.value))
const showSidebar = computed(() => hasSidebar(form.value))
const expanded = computed(() => sidebarExpanded(form.value, prefs.value))
const groups = sidebarNavGroups()
const more = moreItems()
const connected = ref(false)

function savePrefs(): void {
  writeShellPrefs(prefs.value)
}

function toggleSidebar(): void {
  prefs.value = { ...prefs.value, collapsed: !prefs.value.collapsed }
  savePrefs()
}

function toggleNodes(): void {
  prefs.value = { ...prefs.value, nodesOpen: !prefs.value.nodesOpen }
  savePrefs()
}

// 服务端已更新、当前页面是旧构建：重载即拿到新资源（F8）。
function reloadPage(): void {
  window.location.reload()
}

// ---- 连接状态（顶栏 / 侧栏顶部同一份口径） ----
const connState = computed<{ cls: string; label: string }>(() => {
  if (!connected.value) return { cls: 'conn--off', label: t('shell.conn.offline') }
  switch (liveStatus.value) {
    case 'connected':
      return { cls: 'conn--on', label: t('shell.conn.connected') }
    case 'disconnected':
      return { cls: 'conn--down', label: t('shell.conn.disconnected') }
    case 'paused':
      return { cls: 'conn--wait', label: t('shell.conn.paused') }
    default:
      return { cls: 'conn--wait', label: t('shell.conn.reconnecting') }
  }
})

// ---- 运行节点摘要（侧栏底部默认收起，只显示一行） ----
const nodes = ref<Array<{ name: string; online: boolean }>>([])
async function refreshNodes(): Promise<void> {
  try {
    const resp = await listRunners()
    nodes.value = (resp.runners ?? []).map((r) => ({ name: r.name, online: r.status === 'up' }))
  } catch {
    // 只是摘要：拉不到保持上一帧
  }
}
const nodePoller = createPoller(refreshNodes, 30_000)
const nodesOnline = computed(() => nodes.value.filter((n) => n.online).length)

function syncViewport(): void {
  if (typeof window !== 'undefined') viewportW.value = window.innerWidth
}

function logout(): void {
  clearToken()
  connected.value = false
  moreOpen.value = false
  void router.replace({ path: '/access' })
}

watch(
  () => route.path,
  () => {
    moreOpen.value = false
  },
)

onMounted(() => {
  prefs.value = readShellPrefs()
  connected.value = hasToken()
  window.addEventListener('resize', syncViewport)
  nodePoller.start()
})

onUnmounted(() => {
  window.removeEventListener('resize', syncViewport)
  nodePoller.stop()
})
</script>

<template>
  <div
    class="shell"
    :class="{
      'shell--rail': showSidebar && !expanded,
      'shell--tabs': hasTabBar(form),
      'shell--more': moreOpen,
      'shell--fill': route.path === '/mail',
    }"
    data-test="app-shell"
    :data-form="form"
  >
    <!-- 侧栏 / 图标栏 -->
    <aside v-if="showSidebar" class="side" :class="{ 'side--rail': !expanded }" :aria-label="t('shell.nav')">
      <div class="side-head">
        <RouterLink to="/today" class="brand" :title="t('shell.home')">
          <span class="brand-mark" aria-hidden="true">g</span>
          <span v-if="expanded" class="brand-name">gofer</span>
        </RouterLink>
        <span
          v-if="expanded"
          class="conn"
          :class="connState.cls"
          :title="connState.label"
          role="status"
          :aria-label="connState.label"
          data-testid="conn-state"
          ><span class="conn-dot"></span>{{ connState.label }}</span
        >
      </div>

      <nav class="side-nav">
        <section v-for="group in groups" :key="group.key" class="side-group">
          <h2 v-if="expanded" class="side-group-title">{{ t(group.key) }}</h2>
          <RouterLink
            v-for="item in group.items"
            :key="item.to"
            :to="item.to"
            class="side-link"
            :class="{ 'side-link--active': navItemActive(route.path, item.to) }"
            :title="t(item.key)"
            data-test="side-link"
          >
            <Icon :icon="navIcon(item.icon)" size="md" />
            <span v-if="expanded" class="side-label">{{ t(item.key) }}</span>
            <!-- 黄色徽标只给「需要人」的入口（§6.1）：今天 = 决策队列里要我现在处理的件数。 -->
            <span
              v-if="expanded && item.to === '/today' && decisionCount > 0"
              class="side-count"
              :title="t('shell.needsYou', { n: decisionCount })"
              data-testid="today-needs-badge"
              >{{ decisionCount }}</span
            >
            <span
              v-if="expanded && item.to === '/work' && shouldShowWorkBadge(workNeedsMeCount)"
              class="side-count"
              :title="`${workNeedsMeCount}`"
              data-testid="work-needs-me-badge"
              >{{ workBadgeLabel(workNeedsMeCount) }}</span
            >
          </RouterLink>
        </section>
      </nav>

      <div class="side-foot">
        <button
          class="nodes-summary"
          type="button"
          :aria-expanded="prefs.nodesOpen"
          data-test="nodes-summary"
          @click="toggleNodes"
        >
          <span class="nodes-dot" aria-hidden="true"></span>
          <span v-if="expanded">{{ t('shell.nodes.summary', { on: nodesOnline, total: nodes.length }) }}</span>
        </button>
        <ul v-if="prefs.nodesOpen && nodes.length" class="nodes-list" data-test="nodes-list">
          <li v-for="n in nodes" :key="n.name" class="node">
            <span class="node-dot" :class="{ 'node-dot--on': n.online }" aria-hidden="true"></span>
            <span class="node-name">{{ n.name }}</span>
          </li>
        </ul>
        <button
          v-if="form === 'desktop'"
          class="collapse-btn"
          type="button"
          :aria-label="expanded ? t('shell.collapse') : t('shell.expand')"
          :title="expanded ? t('shell.collapse') : t('shell.expand')"
          data-test="sidebar-toggle"
          @click="toggleSidebar"
        >
          <span aria-hidden="true" class="chev"><Icon :icon="expanded ? PanelLeftClose : PanelLeftOpen" size="sm" /></span>
        </button>
      </div>
    </aside>

    <div class="main">
      <!-- 顶栏 -->
      <header class="top">
        <RouterLink v-if="hasTabBar(form)" to="/today" class="brand brand--mobile">
          <span class="brand-mark" aria-hidden="true">g</span>
        </RouterLink>
        <button
          class="search"
          type="button"
          :title="t('shell.search')"
          :aria-label="t('shell.search')"
          data-test="palette-open"
          @click="paletteOpen = true"
        >
          <Icon :icon="Search" size="sm" />
          <span class="search-label">{{ t('shell.search') }}</span>
          <kbd class="kbd search-kbd">Ctrl K</kbd>
        </button>

        <div class="top-right">
          <span
            v-if="hasTabBar(form)"
            class="conn conn--compact"
            :class="connState.cls"
            :title="connState.label"
            role="status"
            :aria-label="connState.label"
            data-testid="conn-state-mobile"
            ><span class="conn-dot"></span
          ></span>
          <span
            v-if="readOnly"
            class="readonly-badge"
            :title="CEILING_READ_ONLY_NOTICE"
            data-testid="readonly-badge"
            >{{ t('shell.readonly') }}</span
          >
          <button v-if="staleBuild" class="stale-build" type="button" :title="t('shell.stale')" @click="reloadPage">
            {{ t('shell.staleShort') }}
          </button>
          <RouterLink
            v-else-if="updateLatest"
            to="/settings/about"
            class="update-avail"
            :title="`gofer ${updateLatest}`"
            data-testid="update-available-badge"
            ><Icon :icon="Sparkles" size="sm" /> {{ updateLatest }}</RouterLink
          >
          <NeedsMeButton />
          <RouterLink v-if="!readOnly" to="/new" class="new-job" :title="t('act.newJob')" active-class="new-job--active">
            <Icon :icon="Plus" size="sm" />
            <span class="new-job-label">{{ t('act.newJob') }}</span>
          </RouterLink>
          <QuickCapture v-if="!hasTabBar(form)" />
          <MailButton />
          <ThemeToggle />
          <LangToggle v-if="!hasTabBar(form)" />
        </div>
      </header>

      <Wire />

      <main
        class="content"
        :class="{
          'content--workbench': route.path === '/workbench',
          'content--fill': route.path === '/mail',
        }"
      >
        <slot />
      </main>

      <!-- 手机底部标签栏 -->
      <nav v-if="hasTabBar(form)" class="tabs" :aria-label="t('shell.nav')">
        <RouterLink
          v-for="item in TAB_BAR_ITEMS"
          :key="item.to"
          :to="item.to"
          class="tab"
          :class="{ 'tab--active': tabBarActive(route.path) === item.to }"
        >
          <Icon :icon="navIcon(item.icon)" size="lg" />
          <span class="tab-label">{{ t(item.key) }}</span>
        </RouterLink>
        <button
          class="tab"
          :class="{ 'tab--active': moreActive(route.path) || moreOpen }"
          type="button"
          :aria-expanded="moreOpen"
          data-test="tab-more"
          @click="moreOpen = !moreOpen"
        >
          <Icon :icon="navIcon('more')" size="lg" />
          <span class="tab-label">{{ t('nav.more') }}</span>
        </button>
      </nav>
    </div>

    <!-- 更多抽屉（手机） -->
    <div v-if="moreOpen && hasTabBar(form)" class="more-scrim" @click.self="moreOpen = false">
      <div class="more" role="dialog" aria-modal="true" :aria-label="t('nav.more')" data-test="more-sheet">
        <div class="more-grid">
          <RouterLink v-for="item in more" :key="item.to" :to="item.to" class="more-item">
            <Icon :icon="navIcon(item.icon)" size="lg" />
            <span class="more-label">{{ t(item.key) }}</span>
          </RouterLink>
        </div>
        <div class="more-actions">
          <ThemeToggle />
          <LangToggle />
          <button class="more-logout" type="button" @click="logout">{{ t('act.logout') }}</button>
        </div>
      </div>
    </div>

    <CommandPalette v-model:open="paletteOpen" />
  </div>
</template>

<style scoped>
.shell {
  display: flex;
  min-height: 100vh;
  background: var(--bg);
  color: var(--ink);
}
.shell--fill {
  height: 100vh;
  overflow: hidden;
}
@supports (height: 100dvh) {
  .shell--fill {
    height: 100dvh;
  }
}
.shell--tabs {
  flex-direction: column;
}

/* ---- 侧栏 ---- */
.side {
  display: flex;
  flex-direction: column;
  width: 236px;
  flex: none;
  background: var(--surface-2);
  border-right: 1px solid var(--line);
  /* 侧栏固定为一屏高并贴住视口：页面再长，导航在 .side-nav 里自己滚，
     底部节点区始终停在窗口底部（不随右侧内容被推到很下面）。 */
  position: sticky;
  top: 0;
  height: 100vh;
  overflow: hidden;
  align-self: flex-start;
}
@supports (height: 100dvh) {
  .side {
    height: 100dvh;
  }
}
.side--rail {
  width: 64px;
}
.side-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  padding: var(--s3) var(--s3);
  min-height: 52px;
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: var(--s2);
  color: var(--ink);
  font-weight: 600;
  font-size: var(--fs-md);
  text-decoration: none;
  white-space: nowrap;
}
.brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  flex: none;
  border-radius: var(--r-chip);
  background: var(--accent);
  color: var(--accent-ink);
  font-weight: 700;
  font-size: var(--fs-sm);
}
.conn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.conn-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--idle);
  flex: none;
}
.conn--on {
  color: var(--done);
}
.conn--on .conn-dot {
  background: var(--done);
}
.conn--wait .conn-dot {
  background: var(--run);
}
.conn--down .conn-dot {
  background: var(--fail);
}

.side-nav {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--s1) var(--s2);
}
.side-group + .side-group {
  margin-top: var(--s3);
  padding-top: var(--s3);
  border-top: 1px solid var(--line);
}
.side-group-title {
  margin: 0 0 var(--s1);
  padding: 0 var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-weight: 600;
  letter-spacing: 0.04em;
}
.side-link {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-height: 32px;
  padding: 0 var(--s2);
  border-radius: var(--r-ctl);
  color: var(--ink-2);
  font-size: var(--fs-sm);
  text-decoration: none;
  white-space: nowrap;
}
.side--rail .side-link {
  justify-content: center;
  padding: 0;
}
.side-link:hover {
  background: var(--surface);
  color: var(--ink);
}
.side-link--active {
  background: var(--accent-soft);
  color: var(--accent);
}
.side-label {
  overflow: hidden;
  text-overflow: ellipsis;
}
.side-count {
  margin-left: auto;
  padding: 0 5px;
  border-radius: 999px;
  background: var(--need-soft);
  color: var(--need-ink);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
}

.side-foot {
  border-top: 1px solid var(--line);
  padding: var(--s2);
  display: flex;
  flex-direction: column;
  gap: var(--s1);
}
.nodes-summary {
  display: flex;
  align-items: center;
  gap: var(--s2);
  background: transparent;
  border: 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  padding: 4px 2px;
  text-align: left;
  white-space: nowrap;
  overflow: hidden;
}
.nodes-summary:hover {
  color: var(--ink);
}
.nodes-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--done);
  flex: none;
}
.side--rail .nodes-summary {
  justify-content: center;
}
.nodes-list {
  list-style: none;
  margin: 0;
  padding: 0 0 0 2px;
  max-height: 30vh;
  overflow-y: auto;
}
.node {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: var(--fs-xs);
  color: var(--ink-2);
  padding: 2px 0;
}
.node-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--idle);
  flex: none;
}
.node-dot--on {
  background: var(--done);
}
.node-name {
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.collapse-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  align-self: stretch;
  min-height: 26px;
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink-3);
  padding: 2px 6px;
}
.collapse-btn:hover {
  color: var(--ink);
  border-color: var(--accent);
}

/* ---- 主区 ---- */
.main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}
.top {
  display: flex;
  align-items: center;
  gap: var(--s3);
  padding: var(--s2) var(--s4);
  background: var(--surface);
  border-bottom: 1px solid var(--line);
  min-height: 52px;
}
.brand--mobile {
  flex: none;
}
.search {
  display: inline-flex;
  align-items: center;
  gap: var(--s2);
  min-width: 200px;
  padding: 5px 10px;
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.search:hover {
  border-color: var(--line-strong);
  color: var(--ink-2);
}
.search-kbd {
  margin-left: auto;
}
.top-right {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin-left: auto;
  min-width: 0;
}
.new-job {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: var(--accent);
  color: var(--accent-ink);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  padding: 5px 10px;
  font-size: var(--fs-sm);
  font-weight: 600;
  white-space: nowrap;
  text-decoration: none;
  flex: none;
}
.new-job:hover {
  background: var(--accent-hover);
}
.readonly-badge {
  border: 1px solid var(--run);
  color: var(--run);
  border-radius: var(--r-ctl);
  padding: 3px 8px;
  font-size: var(--fs-xs);
  white-space: nowrap;
  flex: none;
}
.stale-build {
  background: var(--run);
  color: var(--accent-ink);
  border: 1px solid var(--run);
  border-radius: var(--r-ctl);
  padding: 4px 8px;
  font-size: var(--fs-xs);
  font-weight: 600;
}
.update-avail {
  border: 1px solid var(--done);
  color: var(--done);
  border-radius: var(--r-ctl);
  padding: 3px 8px;
  font-size: var(--fs-xs);
  text-decoration: none;
  white-space: nowrap;
}

.content {
  flex: 1;
  min-width: 0;
  padding: var(--s4);
  overflow-x: auto;
}
.content--workbench {
  padding: 0;
  overflow: hidden;
}
.content--fill {
  min-height: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

/* ---- 手机标签栏 ---- */
.tabs {
  position: sticky;
  bottom: 0;
  display: flex;
  align-items: stretch;
  background: var(--surface);
  border-top: 1px solid var(--line);
  padding-bottom: env(safe-area-inset-bottom, 0);
}
.tab {
  flex: 1 1 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  padding: 7px 2px;
  background: transparent;
  border: 0;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  text-decoration: none;
}
.tab--active {
  color: var(--accent);
}

/* ---- 更多抽屉 ---- */
.more-scrim {
  position: fixed;
  inset: 0;
  z-index: 80;
  background: var(--scrim);
  display: flex;
  align-items: flex-end;
}
.more {
  width: 100%;
  background: var(--surface);
  border-radius: var(--r-sheet) var(--r-sheet) 0 0;
  border-top: 1px solid var(--line-strong);
  padding: var(--s4) var(--s3) var(--s5);
  max-height: 70vh;
  overflow-y: auto;
}
.more-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: var(--s2);
}
.more-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  padding: var(--s3) 4px;
  border-radius: var(--r-panel);
  background: var(--surface-2);
  color: var(--ink);
  font-size: var(--fs-sm);
  text-decoration: none;
}
.more-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}
.more-actions {
  display: flex;
  align-items: center;
  gap: var(--s2);
  margin-top: var(--s4);
  padding-top: var(--s3);
  border-top: 1px solid var(--line);
}
.more-logout {
  margin-left: auto;
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink-2);
  padding: 5px 10px;
  font-size: var(--fs-sm);
}

/* ---- 断点：761–1099 图标栏、≤760 手机 ---- */
@media (max-width: 1099px) {
  .side-link {
    justify-content: center;
    padding: 0;
  }
  .side-group-title {
    display: none;
  }
  .new-job-label {
    display: none;
  }
}
@media (max-width: 760px) {
  .shell {
    min-height: 100dvh;
  }
  .top {
    gap: var(--s2);
    padding: var(--s2) var(--s3);
  }
  .search {
    min-width: 0;
    flex: none;
    padding: 5px 8px;
  }
  .search-label,
  .search-kbd {
    display: none;
  }
  .top-right {
    gap: var(--s1);
  }
  .content {
    padding: var(--s3) var(--s3) var(--s5);
  }
  .conn--compact {
    display: inline-flex;
  }
}
</style>
