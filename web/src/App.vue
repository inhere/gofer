<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { clearToken, hasToken } from './store/auth'
import EscalationBell from './components/EscalationBell.vue'
import TopbarMenu from './components/TopbarMenu.vue'
import { needsReviewCount, reviewBadgeLabel, shouldShowReviewBadge } from './store/reviewCount'
import { staleBuild } from './store/staleBuild'
import { liveStatus } from './api/live'
import { shouldShowWorkBadge, startWorkNeedsMe, stopWorkNeedsMe, workBadgeLabel, workNeedsMeCount } from './store/workNeedsMe'

const router = useRouter()
const route = useRoute()

// 连接态：基于是否有 token 的简单标识（连通性探活留给具体页面/T6）
const connected = ref(false)

// 窄屏抽屉开关
const drawerOpen = ref(false)

// 顶栏状态点反映真实 WS 状态（Q3）：已连接 / 重连中 / 已断开（兜底轮询中）。
const connState = computed<{ cls: string; label: string }>(() => {
  if (!connected.value) return { cls: 'conn--off', label: 'offline' }
  switch (liveStatus.value) {
    case 'connected':
      return { cls: 'conn--on', label: 'connected' }
    case 'disconnected':
      return { cls: 'conn--down', label: 'disconnected (polling)' }
    case 'paused':
      return { cls: 'conn--wait', label: 'paused (tab hidden)' }
    default:
      return { cls: 'conn--wait', label: 'reconnecting' }
  }
})

function refreshConn() {
  connected.value = hasToken()
}

onMounted(() => {
  refreshConn()
})

// 是否展示导航壳（顶栏 + 主内容）：接入页不展示
const showChrome = computed(() => route.path !== '/access')

// 导航「等我」徽标：进入导航壳后订阅 work 主题，离开（/access）时退订。
watch(
  showChrome,
  (on) => {
    if (on) startWorkNeedsMe()
    else stopWorkNeedsMe()
  },
  { immediate: true },
)

// 登录态变化（进入/离开 /access）时刷新左轨与连接态
watch(
  () => route.path,
  () => {
    refreshConn()
    drawerOpen.value = false
  },
)

// 首页入口是左上角 Logo（不再有 Home 菜单项）。
const homeTo = '/dashboard'
// WEB-12：「⚙ 设置」进设置区（/settings 默认重定向到 /settings/config），二级菜单在
// views/settings/SettingsLayout.vue 里。高亮按路径前缀判定——/settings 下的任意子页
// 都算「在设置里」，不必给每个子路由各写一次。
const settingsNav = { to: '/settings', label: '⚙ 设置' }
const settingsActive = computed(() => route.path.startsWith('/settings'))
const workbenchActive = computed(() => route.path === '/workbench')

interface NavItem {
  to: string
  label: string
}

const navGroups: Array<{ label: string; items: NavItem[] }> = [
  {
    label: '观察',
    items: [
      { to: '/workbench', label: 'Workbench' },
      { to: '/work', label: 'Works' },
      { to: '/board', label: 'Board' },
      { to: '/plans', label: 'Plans' },
      { to: '/issues', label: 'Issues' },
      { to: '/sessions', label: 'Sessions' },
      { to: '/workflows', label: 'Workflows' },
      { to: '/schedules', label: 'Schedules' },
    ],
  },
  {
    label: '舰队',
    items: [
      { to: '/agents', label: 'Agents' },
      { to: '/runners', label: 'Runners' },
      { to: '/projects', label: 'Projects' },
    ],
  },
]

function logout() {
  clearToken()
  connected.value = false
  drawerOpen.value = false
  router.replace({ path: '/access' })
}

function closeDrawer() {
  drawerOpen.value = false
}

// F8：服务端换过版本（或 chunk 重载被冷却抑制）时的提示条入口——重载即拿到新构建。
function reloadPage() {
  window.location.reload()
}
</script>

<template>
  <div class="app-root" :class="{ 'app-root--bare': !showChrome }">
    <header v-if="showChrome" class="topbar">
      <div class="brand mono">
        <RouterLink :to="homeTo" class="brand-name" title="回到首页">Gofer</RouterLink>
        <span class="conn" :class="connState.cls" :title="connState.label" role="status" :aria-label="connState.label" data-testid="conn-state">
          <span class="conn-dot"></span>
        </span>
      </div>
      <nav class="nav mono" aria-label="主导航">
        <RouterLink
          v-if="shouldShowReviewBadge(needsReviewCount)"
          to="/review"
          class="nav-review-badge mono"
          :title="`${needsReviewCount} 个 job 待验收`"
        >{{ reviewBadgeLabel(needsReviewCount) }}</RouterLink>
        <span v-for="group in navGroups" :key="group.label" class="grp">
          <span class="glabel">{{ group.label }}</span>
          <RouterLink
            v-for="item in group.items"
            :key="item.to"
            :to="item.to"
            class="nav-link"
            active-class="nav-link--active"
          >
            {{ item.label }}
            <span v-if="item.to === '/work' && shouldShowWorkBadge(workNeedsMeCount)" class="work-badge" :title="`${workNeedsMeCount} 件工作等我处理`" data-testid="work-needs-me-badge">{{ workBadgeLabel(workNeedsMeCount) }}</span>
          </RouterLink>
        </span>
        <RouterLink
          :to="settingsNav.to"
          class="nav-link nav-settings"
          :class="{ 'nav-link--active': settingsActive }"
        >
          {{ settingsNav.label }}
        </RouterLink>
      </nav>
      <div class="topbar-right mono">
        <button
          v-if="staleBuild"
          class="stale-build"
          type="button"
          title="服务端已更新，当前页面加载的是旧版本前端资源"
          @click="reloadPage"
        >
          有新版本，点击刷新
        </button>
        <RouterLink to="/new" class="new-job" active-class="new-job--active">
          <span aria-hidden="true">+</span>
          <span class="new-job-label"><span class="new-job-verb">新建 </span>job</span>
        </RouterLink>
        <RouterLink to="/schedules/new" class="new-job" active-class="new-job--active">
          <span aria-hidden="true">+</span>
          <span class="new-job-label"><span class="new-job-verb">新建 </span>cron</span>
        </RouterLink>
        <EscalationBell />
        <TopbarMenu @logout="logout" />
      </div>
    </header>

    <div v-if="showChrome" class="shell">
      <aside class="drawer-nav" :class="{ 'drawer-nav--open': drawerOpen }" aria-label="移动端主导航">
        <nav class="drawer-nav-inner mono" aria-label="主导航抽屉">
          <RouterLink
            v-if="shouldShowReviewBadge(needsReviewCount)"
            to="/review"
            class="drawer-review-badge mono"
            @click="closeDrawer"
          >{{ reviewBadgeLabel(needsReviewCount) }}</RouterLink>

          <section v-for="group in navGroups" :key="group.label" class="drawer-section">
            <h2 class="drawer-title mono">{{ group.label }}</h2>
            <RouterLink
              v-for="item in group.items"
              :key="item.to"
              :to="item.to"
              class="drawer-link"
              active-class="drawer-link--active"
              @click="closeDrawer"
            >
              {{ item.label }}
              <span v-if="item.to === '/work' && shouldShowWorkBadge(workNeedsMeCount)" class="work-badge" :title="`${workNeedsMeCount} 件工作等我处理`" data-testid="work-needs-me-badge-drawer">{{ workBadgeLabel(workNeedsMeCount) }}</span>
            </RouterLink>
          </section>

          <RouterLink
            :to="settingsNav.to"
            class="drawer-link drawer-link--settings"
            :class="{ 'drawer-link--active': settingsActive }"
            @click="closeDrawer"
          >
            {{ settingsNav.label }}
          </RouterLink>
        </nav>
      </aside>

      <!-- 窄屏抽屉遮罩 -->
      <div
        v-if="drawerOpen"
        class="drawer-scrim"
        aria-hidden="true"
        @click="drawerOpen = false"
      ></div>

      <!-- 手机端主导航入口：左下角浮动按钮（拇指可及；左上角太远不好点）。 -->
      <button
        class="rail-toggle"
        :class="{ 'rail-toggle--lifted': workbenchActive }"
        type="button"
        :aria-label="drawerOpen ? '关闭主导航' : '打开主导航'"
        :aria-expanded="drawerOpen"
        @click="drawerOpen = !drawerOpen"
      >
        <svg width="22" height="22" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
          <path v-if="drawerOpen" d="M6 6l12 12M18 6L6 18" />
          <path v-else d="M4 7h16M4 12h16M4 17h16" />
        </svg>
      </button>
      <main class="content" :class="{ 'content--workbench': workbenchActive }">
        <RouterView />
      </main>
    </div>

    <!-- 接入页：无壳 -->
    <main v-else class="content content--bare">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.app-root {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}

.topbar {
  display: flex;
  align-items: center;
  gap: 24px;
  padding: 10px 18px;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
}

.rail-toggle {
  display: none;
  position: fixed;
  left: 16px;
  bottom: calc(16px + env(safe-area-inset-bottom));
  /* 高于导航抽屉(30)以便再点关闭；低于告警铃(55)与会话抽屉(80)，不遮它们。 */
  z-index: 40;
  width: 48px;
  height: 48px;
  align-items: center;
  justify-content: center;
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: 50%;
  box-shadow: 0 6px 20px rgba(0, 0, 0, 0.45);
  font-size: 20px;
  line-height: 1;
}
/* 工作台底部有输入框，抬高避免压住它。 */
.rail-toggle--lifted {
  bottom: calc(84px + env(safe-area-inset-bottom));
}
.rail-toggle:hover {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.brand {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
  white-space: nowrap;
  font-size: 14px;
  letter-spacing: 0.04em;
}
.brand-name {
  color: var(--paper);
  font-weight: 600;
}
.brand-name:hover {
  color: var(--phosphor);
  text-decoration: none;
}
/* 「等我」徽标：Works 菜单项右上的小胶囊，0 时不渲染。 */
.work-badge {
  display: inline-block;
  min-width: 16px;
  margin-left: 4px;
  padding: 1px 5px;
  border-radius: 999px;
  background: var(--run);
  color: var(--ink);
  font-size: 10px;
  line-height: 14px;
  font-weight: 600;
  text-align: center;
  vertical-align: middle;
}

.nav {
  display: flex;
  align-items: center;
  gap: 16px;
  font-size: 13px;
}
.grp {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  min-width: max-content;
}
.glabel {
  flex: none;
  color: var(--queue);
  border-left: 1px solid var(--line);
  padding-left: 10px;
  font-size: 10px;
  letter-spacing: 0.08em;
  opacity: 0.75;
  white-space: nowrap;
}
.nav-link {
  color: var(--queue);
  padding: 2px 0;
  border-bottom: 1px solid transparent;
  white-space: nowrap;
}
.nav-link:hover {
  color: var(--paper);
  text-decoration: none;
}
.nav-link--active {
  color: var(--phosphor);
  border-bottom-color: var(--phosphor);
}
.nav-settings {
  border-left: 1px solid var(--line);
  padding-left: 14px;
  margin-left: 2px;
}
/* REV-01：待验收数是独立入口，使用与顶栏控件同高的强调色胶囊。 */
.nav-review-badge,
.drawer-review-badge {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--run);
  color: var(--ink);
  font-size: 11px;
  line-height: 1;
  font-weight: 600;
  white-space: nowrap;
}
.nav-review-badge:hover,
.drawer-review-badge:hover {
  color: var(--ink);
  text-decoration: none;
  opacity: 0.9;
}
.drawer-review-badge {
  margin: 0 6px 8px;
}

.topbar-right {
  flex: none;
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}

.new-job {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
  flex: none;
}
.new-job:hover {
  text-decoration: none;
  opacity: 0.9;
}
.new-job--active {
  opacity: 0.85;
}

/* F8：服务端已更新，当前页面是旧构建——提示条可点，点了重载。 */
.stale-build {
  background: var(--run);
  color: var(--ink);
  border: 1px solid var(--run);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
  font-weight: 600;
  font-family: var(--font-mono);
}
.stale-build:hover {
  opacity: 0.9;
}

.conn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.conn-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--queue);
}
.conn--on .conn-dot {
  background: var(--done);
}
.conn--on {
  color: var(--done);
}
.conn--off {
  color: var(--queue);
}
.conn--wait .conn-dot {
  background: var(--run);
}
.conn--wait {
  color: var(--run);
}
.conn--down .conn-dot {
  background: var(--fail);
}
.conn--down {
  color: var(--fail);
}

/* 壳：主区单栏 */
.shell {
  flex: 1;
  min-height: 0;
}

.drawer-nav {
  display: none;
  background: var(--panel);
  border-right: 1px solid var(--line);
  overflow-y: auto;
}

.drawer-nav-inner {
  padding: 14px 10px;
}
.drawer-section {
  margin-top: 18px;
}
.drawer-title {
  font-size: 11px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0 6px 8px;
  text-transform: uppercase;
}
.drawer-link {
  display: block;
  color: var(--paper);
  border: 1px solid transparent;
  border-radius: var(--radius);
  padding: 7px 8px;
  font-size: 12px;
  white-space: nowrap;
}
.drawer-link:hover {
  background: var(--ink);
  text-decoration: none;
}
.drawer-link .work-badge {
  float: right;
}
.drawer-link--active {
  color: var(--phosphor);
  border-color: var(--line);
  background: var(--ink);
}
.drawer-link--settings {
  border-top: 1px solid var(--line);
  margin-top: 18px;
  padding-top: 12px;
}

.drawer-scrim {
  display: none;
}

.content {
  flex: 1;
  padding: 18px;
  min-width: 0;
  overflow-x: auto;
}
.content--bare {
  flex: 1;
  padding: 18px;
}
.content--workbench {
  padding: 0;
  overflow: hidden;
}

/* 响应式：窄屏左轨折叠为抽屉 */
/* 中等宽度：顶栏整行放不下时先去掉副标题与「新建」二字、收紧间距；再窄就让导航自己
   横向滚动，而不是把按钮挤成多行、把整页撑出横向滚动条（1280px 实测问题）。 */
@media (max-width: 1440px) {
  .topbar {
    gap: 14px;
  }
  .nav {
    gap: 10px;
  }
  .new-job-verb {
    display: none;
  }
}
@media (min-width: 769px) and (max-width: 1100px) {
  .nav {
    flex: 1 1 auto;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: thin;
  }
}

@media (max-width: 768px) {
  .rail-toggle {
    display: inline-flex;
  }
  .topbar {
    gap: 10px;
    padding: 10px 12px;
  }
  .nav {
    display: none;
  }
  .topbar-right {
    gap: 8px;
  }
  .new-job {
    padding: 4px 8px;
  }
  .new-job-verb {
    display: none;
  }
  .drawer-nav {
    display: block;
    position: fixed;
    top: 0;
    left: 0;
    bottom: 0;
    z-index: 30;
    width: 240px;
    transform: translateX(-100%);
    transition: transform 0.2s ease;
  }
  .drawer-nav--open {
    transform: translateX(0);
  }
  .drawer-scrim {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 20;
    background: rgba(0, 0, 0, 0.45);
  }
  .content {
    padding: 14px 12px;
  }
  .content--workbench {
    padding: 0;
  }
}
</style>
