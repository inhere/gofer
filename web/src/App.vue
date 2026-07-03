<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { clearToken, hasToken } from './store/auth'
import { listAgents, listPresence, listProjects } from './api/client'
import type { AgentInfo, Presence } from './api/types'
import EscalationBell from './components/EscalationBell.vue'
import ThemeToggle from './components/ThemeToggle.vue'

const router = useRouter()
const route = useRoute()

// 连接态：基于是否有 token 的简单标识（连通性探活留给具体页面/T6）
const connected = ref(false)

// 左轨数据
const projects = ref<string[]>([])
const agents = ref<AgentInfo[]>([])
const driverOnlineCount = ref(0)
const supervisorOnline = ref(false)
const railError = ref('')
// 窄屏抽屉开关
const drawerOpen = ref(false)

function refreshConn() {
  connected.value = hasToken()
}

// 拉取左轨数据：失败静默（仅小错误提示），不阻塞主区
async function loadRail() {
  if (!hasToken()) {
    projects.value = []
    agents.value = []
    driverOnlineCount.value = 0
    supervisorOnline.value = false
    return
  }
  railError.value = ''
  const [prResult, arResult, drResult] = await Promise.allSettled([
    listProjects(),
    listAgents(),
    listPresence(),
  ])
  if (prResult.status === 'fulfilled' && arResult.status === 'fulfilled') {
    const pr = prResult.value
    const ar = arResult.value
    projects.value = pr.projects ?? []
    agents.value = ar.agents ?? []
  } else {
    // 401 已由 client 处理（跳转登录）；其余仅在左轨给出轻提示
    const e = prResult.status === 'rejected' ? prResult.reason : arResult.status === 'rejected' ? arResult.reason : ''
    railError.value = e instanceof Error ? e.message : String(e)
  }
  if (drResult.status === 'fulfilled') {
    const online = (drResult.value.agents ?? []).filter(isDriverOnline)
    driverOnlineCount.value = online.length
    supervisorOnline.value = online.some((a) => a.role === 'supervisor')
  } else if (!railError.value) {
    const e = drResult.reason
    railError.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(() => {
  refreshConn()
  void loadRail()
})

// 是否展示导航壳（顶栏 + 左轨）：接入页不展示
const showChrome = computed(() => route.path !== '/access')

// 登录态变化（进入/离开 /access）时刷新左轨与连接态
watch(
  () => route.path,
  () => {
    refreshConn()
    if (showChrome.value && projects.value.length === 0 && agents.value.length === 0) {
      void loadRail()
    }
    drawerOpen.value = false
  },
)

// 当前看板过滤的 project（用于左轨高亮）
const activeProject = computed(() => {
  const p = route.query.project
  return typeof p === 'string' && p ? p : ''
})

const homeNav = { to: '/dashboard', label: 'Home' }
const navGroups = [
  {
    label: '观察',
    items: [
      { to: '/board', label: 'Board' },
      { to: '/workflows', label: 'Workflows' },
      { to: '/schedules', label: 'Schedules' },
    ],
  },
  {
    label: '舰队',
    items: [
      { to: '/drivers', label: 'Drivers' },
      { to: '/agents', label: 'Agents' },
      { to: '/runners', label: 'Runners' },
      { to: '/cluster', label: 'Cluster' },
      { to: '/projects', label: 'Projects' },
      { to: '/config', label: '配置' },
    ],
  },
]

function logout() {
  clearToken()
  connected.value = false
  projects.value = []
  agents.value = []
  driverOnlineCount.value = 0
  supervisorOnline.value = false
  router.replace({ path: '/access' })
}

// 左轨 Project 点击 -> 看板过滤
function selectProject(key: string) {
  drawerOpen.value = false
  void router.push({ path: '/board', query: { project: key } })
}
// 清除看板过滤（“全部”）
function clearProjectFilter() {
  drawerOpen.value = false
  void router.push({ path: '/board' })
}
// 左轨 Agent 点击 -> agents 页
function gotoAgents() {
  drawerOpen.value = false
  void router.push('/agents')
}
function gotoDrivers() {
  drawerOpen.value = false
  void router.push('/drivers')
}

// agent 状态 -> 视觉 token（available=done，不可用=fail/queue）
function agentDotColor(a: AgentInfo): string {
  return a.available ? 'var(--done)' : 'var(--fail)'
}
function isDriverOnline(a: Presence): boolean {
  return a.status !== 'stale'
}
</script>

<template>
  <div class="app-root" :class="{ 'app-root--bare': !showChrome }">
    <header v-if="showChrome" class="topbar">
      <button
        class="rail-toggle"
        type="button"
        aria-label="切换侧栏"
        :aria-expanded="drawerOpen"
        @click="drawerOpen = !drawerOpen"
      >
        &#9776;
      </button>
      <div class="brand mono">
        <span class="brand-name">Gofer</span>
        <span class="brand-sep">&#9656;</span>
        <span class="brand-sub">agent bridge</span>
      </div>
      <nav class="nav mono" aria-label="主导航">
        <RouterLink
          :to="homeNav.to"
          class="nav-link"
          active-class="nav-link--active"
        >
          {{ homeNav.label }}
        </RouterLink>
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
          </RouterLink>
        </span>
      </nav>
      <div class="topbar-right mono">
        <RouterLink to="/new" class="new-job" active-class="new-job--active">
          + 新建 job
        </RouterLink>
        <RouterLink to="/schedules/new" class="new-job" active-class="new-job--active">
          + 新建 cron
        </RouterLink>
        <EscalationBell />
        <span class="conn" :class="connected ? 'conn--on' : 'conn--off'">
          <span class="conn-dot"></span>
          {{ connected ? 'connected' : 'offline' }}
        </span>
        <ThemeToggle />
        <button class="logout" type="button" @click="logout">登出</button>
      </div>
    </header>

    <div v-if="showChrome" class="shell">
      <aside class="rail" :class="{ 'rail--open': drawerOpen }" aria-label="侧栏导航">
        <!-- PROJECTS -->
        <section class="rail-section">
          <h2 class="rail-title mono">PROJECTS</h2>
          <ul class="rail-list">
            <li>
              <button
                type="button"
                class="rail-item rail-item--all mono"
                :class="{ 'rail-item--active': !activeProject }"
                @click="clearProjectFilter"
              >
                全部
              </button>
            </li>
            <li v-for="key in projects" :key="key">
              <button
                type="button"
                class="rail-item mono"
                :class="{ 'rail-item--active': key === activeProject }"
                :title="key"
                @click="selectProject(key)"
              >
                {{ key }}
              </button>
            </li>
            <li v-if="projects.length === 0" class="rail-empty mono">无项目</li>
          </ul>
        </section>

        <!-- DRIVERS -->
        <section class="rail-section">
          <h2 class="rail-title mono">DRIVERS</h2>
          <button
            type="button"
            class="rail-item rail-item--driver mono"
            @click="gotoDrivers"
          >
            <span
              class="driver-dot"
              :class="{ 'driver-dot--sup': supervisorOnline }"
              aria-hidden="true"
            ></span>
            <span class="driver-label">online</span>
            <span class="driver-count">{{ driverOnlineCount }}</span>
          </button>
        </section>

        <!-- AGENTS -->
        <section class="rail-section">
          <h2 class="rail-title mono">AGENTS</h2>
          <ul class="rail-list">
            <li v-for="a in agents" :key="a.key">
              <button
                type="button"
                class="rail-item rail-item--agent mono"
                :title="a.available ? `${a.type} · available` : (a.error || `${a.type} · unavailable`)"
                @click="gotoAgents"
              >
                <span
                  class="agent-dot"
                  :class="a.available ? 'agent-dot--on' : 'agent-dot--off'"
                  :style="{ background: agentDotColor(a) }"
                  aria-hidden="true"
                ></span>
                <span class="agent-key">{{ a.key }}</span>
                <span class="agent-type">{{ a.type }}</span>
              </button>
            </li>
            <li v-if="agents.length === 0" class="rail-empty mono">无 agent</li>
          </ul>
        </section>

        <p v-if="railError" class="rail-error mono" :title="railError">侧栏加载失败</p>
      </aside>

      <!-- 窄屏抽屉遮罩 -->
      <div
        v-if="drawerOpen"
        class="rail-scrim"
        aria-hidden="true"
        @click="drawerOpen = false"
      ></div>

      <main class="content">
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
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 8px;
  font-size: 14px;
  line-height: 1;
}
.rail-toggle:hover {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

.brand {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  letter-spacing: 0.04em;
}
.brand-name {
  color: var(--paper);
  font-weight: 600;
}
.brand-sep {
  color: var(--phosphor);
}
.brand-sub {
  color: var(--queue);
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

.topbar-right {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 14px;
  font-size: 12px;
}

.new-job {
  background: var(--phosphor);
  color: var(--ink);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
  font-weight: 600;
}
.new-job:hover {
  text-decoration: none;
  opacity: 0.9;
}
.new-job--active {
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

.logout {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.logout:hover {
  border-color: var(--phosphor);
  color: var(--phosphor);
}

/* 壳：左轨 + 主区 */
.shell {
  flex: 1;
  display: flex;
  align-items: stretch;
  min-height: 0;
}

.rail {
  width: 232px;
  flex: none;
  background: var(--panel);
  border-right: 1px solid var(--line);
  padding: 14px 10px;
  overflow-y: auto;
}

.rail-section {
  margin-bottom: 18px;
}
.rail-title {
  font-size: 11px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0 6px 8px;
  text-transform: uppercase;
}
.rail-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.rail-item {
  display: block;
  width: 100%;
  text-align: left;
  background: transparent;
  color: var(--paper);
  border: 1px solid transparent;
  border-radius: var(--radius);
  padding: 6px 8px;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rail-item:hover {
  background: var(--ink);
}
.rail-item--active {
  color: var(--phosphor);
  border-color: var(--line);
  background: var(--ink);
}
.rail-item--all {
  color: var(--queue);
}

.rail-item--agent {
  display: flex;
  align-items: center;
  gap: 7px;
}
.agent-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex: none;
}
.agent-dot--off {
  opacity: 0.85;
  box-shadow: 0 0 0 1px var(--line);
}
.agent-key {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.agent-type {
  color: var(--queue);
  font-size: 11px;
  margin-left: auto;
  flex: none;
}

.rail-item--driver {
  display: flex;
  align-items: center;
  gap: 8px;
}
.driver-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
  background: var(--done);
}
.driver-dot--sup {
  background: var(--phosphor);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--phosphor) 25%, transparent);
}
.driver-label {
  color: var(--paper);
}
.driver-count {
  margin-left: auto;
  color: var(--phosphor);
  font-weight: 600;
}

.rail-empty {
  color: var(--queue);
  font-size: 11px;
  padding: 4px 8px;
  opacity: 0.7;
}
.rail-error {
  color: var(--fail);
  font-size: 11px;
  padding: 4px 6px;
}

.rail-scrim {
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

/* 响应式：窄屏左轨折叠为抽屉 */
@media (max-width: 768px) {
  .rail-toggle {
    display: inline-block;
  }
  .topbar {
    gap: 14px;
    padding: 10px 12px;
  }
  .nav {
    display: none;
  }
  .rail {
    position: fixed;
    top: 0;
    left: 0;
    bottom: 0;
    z-index: 30;
    width: 240px;
    transform: translateX(-100%);
    transition: transform 0.2s ease;
  }
  .rail--open {
    transform: translateX(0);
  }
  .rail-scrim {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 20;
    background: rgba(0, 0, 0, 0.45);
  }
  .content {
    padding: 14px 12px;
  }
}
</style>
