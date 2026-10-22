<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { clearToken, hasToken } from './store/auth'
import EscalationBell from './components/EscalationBell.vue'
import EnrollmentBell from './components/EnrollmentBell.vue'
import MailButton from './components/MailButton.vue'
import NavMore from './components/NavMore.vue'
import QuickCapture from './components/QuickCapture.vue'
import TopbarMenu from './components/TopbarMenu.vue'
import DecisionOverlay from './components/today/DecisionOverlay.vue'
import HandledDrawer from './components/today/HandledDrawer.vue'
import UndoToast from './components/today/UndoToast.vue'
import { installTodayGlobals, startToday, stopToday } from './store/today'
import { drawerNavGroups, navPrimary } from './utils/nav'
import { PAGE } from './utils/uiText'
import { staleBuild } from './store/staleBuild'
import { useWhoami } from './store/whoami'
import { CEILING_READ_ONLY_NOTICE } from './utils/ceiling'
import { liveStatus } from './api/live'
import { shouldShowWorkBadge, startWorkNeedsMe, stopWorkNeedsMe, workBadgeLabel, workNeedsMeCount } from './store/workNeedsMe'
import { startUpdateAvailable, stopUpdateAvailable, updateLatest } from './store/updateAvailable'
import {
  NAV_TAB_SCROLL_RESTORE_MS,
  clampTabTop,
  defaultNavTabStore,
  dragPassedThreshold,
  isTypingTarget,
  pctToTop,
  posFromPoint,
  readNavTab,
  writeNavTab,
  type NavTabPos,
} from './utils/navTab'

const router = useRouter()
const route = useRoute()

// 连接态：基于是否有 token 的简单标识（连通性探活留给具体页面/T6）
const connected = ref(false)

// 窄屏抽屉开关
const drawerOpen = ref(false)

// 手机端主导航入口是贴边的半圆小标签（左下角浮动按钮会压住工作台输入框里的 📎）。
// 位置 = 贴哪一侧 + 垂直百分比，可以按住拖动改，记在 localStorage 里。
const navTab = ref<NavTabPos>(readNavTab(defaultNavTabStore()))
const viewportH = ref(typeof window === 'undefined' ? 0 : window.innerHeight)
// 打字 / 滚动时让开；导航抽屉展开时也藏起来（会压住菜单项，点遮罩或菜单项即关闭）。
const typing = ref(false)
const scrolling = ref(false)
// 拖动中的标签中心（视口坐标）；null 表示没在拖。
const dragPoint = ref<{ x: number; y: number } | null>(null)
let dragGrab: { pointerId: number; startX: number; startY: number; dx: number; dy: number; moved: boolean } | null = null
let scrollTimer: ReturnType<typeof setTimeout> | null = null
// pointerup 判定为点按（没过拖动阈值），等随后的 click 去开关抽屉。
let tapPending = false

const tabTop = computed(() => pctToTop(navTab.value.pct, viewportH.value))
const tabAway = computed(() => drawerOpen.value || typing.value || scrolling.value)
const tabStyle = computed<Record<string, string>>(() => {
  const p = dragPoint.value
  if (p) {
    // 跟手：标签中心跟着指针走，保留按下时的抓取偏移，免得跳一下；
    // 垂直方向拖动中就夹在可用区间里，到边界会停住，而不是松手后才弹回去。
    const y = clampTabTop(p.y, viewportH.value)
    return { left: `${p.x}px`, top: `${y}px`, right: 'auto', transform: 'translate(-50%, -50%)' }
  }
  const top = `${tabTop.value}px`
  return navTab.value.side === 'left'
    ? { left: '0px', top, right: 'auto', transform: 'translateY(-50%)' }
    : { left: 'auto', top, right: '0px', transform: 'translateY(-50%)' }
})

function syncViewportH() {
  if (typeof window !== 'undefined') viewportH.value = window.innerHeight
}

function onTabPointerDown(e: PointerEvent) {
  if (e.pointerType === 'mouse' && e.button !== 0) return
  const el = e.currentTarget as HTMLElement | null
  if (!el) return
  el.setPointerCapture?.(e.pointerId)
  const rect = el.getBoundingClientRect()
  dragGrab = {
    pointerId: e.pointerId,
    startX: e.clientX,
    startY: e.clientY,
    dx: rect.left + rect.width / 2 - e.clientX,
    dy: rect.top + rect.height / 2 - e.clientY,
    moved: false,
  }
}

function onTabPointerMove(e: PointerEvent) {
  const grab = dragGrab
  if (!grab || e.pointerId !== grab.pointerId) return
  if (!grab.moved) {
    if (!dragPassedThreshold(grab.startX, grab.startY, e.clientX, e.clientY)) return
    grab.moved = true
  }
  dragPoint.value = { x: e.clientX + grab.dx, y: e.clientY + grab.dy }
}

function onTabPointerUp(e: PointerEvent) {
  const grab = dragGrab
  if (!grab || e.pointerId !== grab.pointerId) return
  dragGrab = null
  const point = dragPoint.value
  dragPoint.value = null
  if (!grab.moved || !point) {
    // 没超过阈值：当点击用，但开关抽屉留给随后的 click 做。若在这里就打开，标签随即隐藏、
    // 遮罩出现，手机上点按合成的 click 会按位置落到遮罩上，又把抽屉关掉（看起来点了没反应）。
    tapPending = true
    return
  }
  const pos = posFromPoint(point.x, point.y, window.innerWidth, window.innerHeight)
  navTab.value = pos
  writeNavTab(pos, defaultNavTabStore())
}

function onTabPointerCancel() {
  dragGrab = null
  tapPending = false
  dragPoint.value = null
}

function onTabClick(e: MouseEvent) {
  // 指针点按（pointerup 判定没拖动）与键盘 Enter / Space（click 的 detail 是 0）在这里开关抽屉；
  // 拖动后松手产生的 click 不算点按。
  const tap = tapPending
  tapPending = false
  if (e.detail !== 0 && !tap) return
  drawerOpen.value = !drawerOpen.value
}

function onFocusIn(e: FocusEvent) {
  if (isTypingTarget(e.target as HTMLElement | null)) typing.value = true
}

function onFocusOut(e: FocusEvent) {
  if (!isTypingTarget(e.target as HTMLElement | null)) return
  // 焦点在输入框之间挪、或同一输入框重绘时先别闪一下：等真正拿到焦点的元素定下来再看。
  setTimeout(() => {
    if (!isTypingTarget(document.activeElement as HTMLElement | null)) typing.value = false
  }, 0)
}

function onScroll() {
  scrolling.value = true
  if (scrollTimer) clearTimeout(scrollTimer)
  scrollTimer = setTimeout(() => {
    scrolling.value = false
    scrollTimer = null
  }, NAV_TAB_SCROLL_RESTORE_MS)
}

// 顶栏状态点反映真实 WS 状态（Q3）：已连接 / 重连中 / 已断开（兜底轮询中）。
const connState = computed<{ cls: string; label: string }>(() => {
  if (!connected.value) return { cls: 'conn--off', label: '离线' }
  switch (liveStatus.value) {
    case 'connected':
      return { cls: 'conn--on', label: '已连接' }
    case 'disconnected':
      return { cls: 'conn--down', label: '已断开（轮询中）' }
    case 'paused':
      return { cls: 'conn--wait', label: '已暂停（标签页隐藏）' }
    default:
      return { cls: 'conn--wait', label: '重连中' }
  }
})

function refreshConn() {
  connected.value = hasToken()
}

onMounted(() => {
  refreshConn()
})

// 贴边标签的全局让开逻辑：焦点进输入框、页面滚动时淡出；窗口尺寸变了重算夹取。
// scroll 在 capture 阶段听，才能收到内层滚动容器（scroll 不冒泡，但捕获阶段会经过 window）。
onMounted(() => {
  window.addEventListener('resize', syncViewportH)
  window.addEventListener('focusin', onFocusIn)
  window.addEventListener('focusout', onFocusOut)
  window.addEventListener('scroll', onScroll, { passive: true, capture: true })
})

onUnmounted(() => {
  window.removeEventListener('resize', syncViewportH)
  window.removeEventListener('focusin', onFocusIn)
  window.removeEventListener('focusout', onFocusOut)
  window.removeEventListener('scroll', onScroll, { capture: true })
  if (scrollTimer) clearTimeout(scrollTimer)
})

// 是否展示导航壳（顶栏 + 主内容）：接入页不展示
const showChrome = computed(() => route.path !== '/access')

// 导航「等我」徽标与「待我决策」队列：进入导航壳后订阅，离开（/access）时退订。
watch(
  showChrome,
  (on) => {
    if (on) {
      startWorkNeedsMe()
      startToday()
      startUpdateAvailable()
    } else {
      stopWorkNeedsMe()
      stopToday()
      stopUpdateAvailable()
    }
  },
  { immediate: true },
)

// N3：全局快捷键 g d 打开「待我决策」浮层、z 撤销；页面卸载 / 换路由时立即提交撤销窗口。
installTodayGlobals(router)

// 登录态变化（进入/离开 /access）时刷新左轨与连接态
watch(
  () => route.path,
  () => {
    refreshConn()
    drawerOpen.value = false
  },
)

// 首页入口是左上角 Logo：N3 起默认落地页是「今天」。
const homeTo = '/today'

// 调用方上限（ceiling）只读：写入口（新建 job 等）一律不摆出来，顶栏挂一个「只读」标记。
const { readOnly } = useWhoami()
// WEB-12：「⚙ 设置」进设置区（/settings 默认重定向到 /settings/config），二级菜单在
// views/settings/SettingsLayout.vue 里。高亮按路径前缀判定——/settings 下的任意子页
// 都算「在设置里」，不必给每个子路由各写一次。
const settingsNav = { to: '/settings', label: `⚙ ${PAGE.settings}` }
const settingsActive = computed(() => route.path.startsWith('/settings'))
const workbenchActive = computed(() => route.path === '/workbench')
// 信箱页要占满顶栏以下的视口：左对话列表与右时间线各自在自己的区域里滚，页面本身不出现
// 外层滚动条。做法与工作台（content--workbench）同一套：给壳加一个「填满视口」的修饰类。
const fillContent = computed(() => route.path === '/mail')


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
  <div class="app-root" :class="{ 'app-root--bare': !showChrome, 'app-root--fill': showChrome && fillContent }">
    <header v-if="showChrome" class="topbar">
      <div class="brand mono">
        <RouterLink :to="homeTo" class="brand-name" title="回到首页">Gofer</RouterLink>
        <span class="conn" :class="connState.cls" :title="connState.label" role="status" :aria-label="connState.label" data-testid="conn-state">
          <span class="conn-dot"></span>
        </span>
      </div>
      <nav class="nav mono" aria-label="主导航">
        <RouterLink
          v-for="item in navPrimary"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          active-class="nav-link--active"
        >
          {{ item.label }}
          <span v-if="item.to === '/work' && shouldShowWorkBadge(workNeedsMeCount)" class="work-badge" :title="`${workNeedsMeCount} 件工作等我处理`" data-testid="work-needs-me-badge">{{ workBadgeLabel(workNeedsMeCount) }}</span>
        </RouterLink>
        <NavMore />
        <RouterLink
          :to="settingsNav.to"
          class="nav-link nav-settings"
          :class="{ 'nav-link--active': settingsActive }"
        >
          {{ settingsNav.label }}
        </RouterLink>
      </nav>
      <div class="topbar-right mono">
        <span
          v-if="readOnly"
          class="readonly-badge"
          :title="CEILING_READ_ONLY_NOTICE"
          data-testid="readonly-badge"
        >
          只读
        </span>
        <button
          v-if="staleBuild"
          class="stale-build"
          type="button"
          title="服务端已更新，当前页面加载的是旧版本前端资源"
          @click="reloadPage"
        >
          有新版本，点击刷新
        </button>
        <RouterLink
          v-else-if="updateLatest"
          to="/settings/about"
          class="update-avail"
          :title="`gofer ${updateLatest} 已发布，点击查看与更新`"
          data-testid="update-available-badge"
        >
          ↑ {{ updateLatest }}
        </RouterLink>
        <RouterLink v-if="!readOnly" to="/new" class="new-job" active-class="new-job--active">
          <span aria-hidden="true">+</span>
          <span class="new-job-label"><span class="new-job-verb">新建 </span>job</span>
        </RouterLink>
        <QuickCapture />
        <MailButton />
        <EnrollmentBell />
        <EscalationBell />
        <TopbarMenu @logout="logout" />
      </div>
    </header>

    <div v-if="showChrome" class="shell">
      <aside class="drawer-nav" :class="{ 'drawer-nav--open': drawerOpen }" aria-label="移动端主导航">
        <nav class="drawer-nav-inner mono" aria-label="主导航抽屉">
          <section v-for="group in drawerNavGroups" :key="group.label" class="drawer-section">
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

      <!-- 手机端主导航入口：贴在屏幕边缘的半圆小标签（可按住拖动，松手吸附到最近一侧）。
           顶栏左上角单手够不着，所以不放顶栏。 -->
      <button
        class="rail-toggle"
        :class="{ 'rail-toggle--right': navTab.side === 'right', 'rail-toggle--dragging': dragPoint, 'rail-toggle--away': tabAway }"
        type="button"
        :style="tabStyle"
        :aria-label="drawerOpen ? '关闭主导航' : '打开主导航'"
        :aria-expanded="drawerOpen"
        @pointerdown="onTabPointerDown"
        @pointermove="onTabPointerMove"
        @pointerup="onTabPointerUp"
        @pointercancel="onTabPointerCancel"
        @click="onTabClick"
      >
        <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
          <path v-if="drawerOpen" d="M6 6l12 12M18 6L6 18" />
          <path v-else d="M4 7h16M4 12h16M4 17h16" />
        </svg>
      </button>
      <main class="content" :class="{ 'content--workbench': workbenchActive, 'content--fill': fillContent }">
        <RouterView />
      </main>
    </div>

    <template v-if="showChrome">
      <DecisionOverlay />
      <HandledDrawer />
      <UndoToast />
    </template>

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
/* 填满视口的页面（信箱）：壳的高度锁在视口里，滚动交给页内的列表 / 时间线。 */
.app-root--fill {
  height: 100vh;
  overflow: hidden;
}
@supports (height: 100dvh) {
  .app-root--fill {
    height: 100dvh;
  }
}

.topbar {
  display: flex;
  align-items: center;
  gap: 24px;
  padding: 10px 18px;
  background: var(--panel);
  border-bottom: 1px solid var(--line);
}

/* 手机端主导航入口：贴在屏幕边缘的半圆小标签（24×48，圆弧朝屏幕内侧）。
   原本是左下角 48×48 的浮动按钮，会压住工作台输入框里的 📎，所以改成贴边标签；
   位置（side + pct）由 App.vue 内联给出，按住可拖动、松手吸附到最近一侧。
   高于导航抽屉(30)以便再点关闭；低于告警铃(55)与会话抽屉(80)，不遮它们。 */
.rail-toggle {
  display: none;
  position: fixed;
  z-index: 40;
  width: 24px;
  height: 48px;
  padding: 0;
  align-items: center;
  justify-content: center;
  background: var(--panel);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: 0 999px 999px 0;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.45);
  line-height: 1;
  /* 按住拖动时不要连带滚动页面；只加在标签上。 */
  touch-action: none;
  cursor: grab;
  transition: opacity 0.15s ease;
  transform: translateY(-50%);
}
.rail-toggle--right {
  border-radius: 999px 0 0 999px;
}
.rail-toggle--dragging {
  cursor: grabbing;
  border-radius: 999px;
  opacity: 1;
}
/* 打字 / 滚动 / 导航抽屉展开时让开：淡出且不接收点击。 */
.rail-toggle--away {
  opacity: 0;
  pointer-events: none;
}
.rail-toggle:hover {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
/* 尊重系统的「减少动态效果」：不要淡入淡出。 */
@media (prefers-reduced-motion: reduce) {
  .rail-toggle {
    transition: none;
  }
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

/* 调用方上限只读：不是错误，只是这个 token 不能写——做成安静的次要徽标。 */
.readonly-badge {
  background: transparent;
  color: var(--run);
  border: 1px solid var(--run);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
  white-space: nowrap;
  flex: none;
  cursor: default;
}
.update-avail {
  color: var(--done);
  border: 1px solid var(--done);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 12px;
  font-family: var(--font-mono);
  text-decoration: none;
  white-space: nowrap;
}
.update-avail:hover {
  opacity: 0.85;
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
/* 填满视口时主内容区是壳里的弹性列，高度由壳夹住（否则 .content 只按内容撑高）。 */
.app-root--fill .shell {
  display: flex;
  flex-direction: column;
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
/* 信箱：主区自己不滚，而是变成一个定高的列容器，页内的两栏各滚各的。 */
.content--fill {
  flex: 1 1 auto;
  min-height: 0;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

/* 响应式：窄屏左轨折叠为抽屉 */
/* 中等宽度：顶栏整行放不下时先去掉副标题与「新建」二字、收紧间距。主导航行只剩 6 项 + 「更多」
   + 设置，1280px 一行放得下，不再需要让导航自己横向滚动。 */
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

/* 900px 以下顶栏改用抽屉：主导航 6 项 +「更多」+ 设置与右侧按钮约需 850px，769–900px 一行放不下；
   而导航不能改成横向滚动兜底（会裁掉「更多」的下拉面板）。 */
@media (max-width: 900px) {
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
