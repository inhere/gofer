<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { clearToken, hasToken } from './store/auth'
import EscalationBell from './components/EscalationBell.vue'
import TopbarMenu from './components/TopbarMenu.vue'

const router = useRouter()
const route = useRoute()

// 连接态：基于是否有 token 的简单标识（连通性探活留给具体页面/T6）
const connected = ref(false)

// 窄屏抽屉开关
const drawerOpen = ref(false)

function refreshConn() {
  connected.value = hasToken()
}

onMounted(() => {
  refreshConn()
})

// 是否展示导航壳（顶栏 + 主内容）：接入页不展示
const showChrome = computed(() => route.path !== '/access')

// 登录态变化（进入/离开 /access）时刷新左轨与连接态
watch(
  () => route.path,
  () => {
    refreshConn()
    drawerOpen.value = false
  },
)

const homeNav = { to: '/dashboard', label: 'Home' }
const settingsNav = { to: '/config', label: '⚙ 设置' }
const navGroups = [
  {
    label: '观察',
    items: [
      { to: '/board', label: 'Board' },
      { to: '/sessions', label: 'Sessions' },
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
</script>

<template>
  <div class="app-root" :class="{ 'app-root--bare': !showChrome }">
    <header v-if="showChrome" class="topbar">
      <button
        class="rail-toggle"
        type="button"
        aria-label="打开主导航"
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
        <RouterLink
          :to="settingsNav.to"
          class="nav-link nav-settings"
          active-class="nav-link--active"
        >
          {{ settingsNav.label }}
        </RouterLink>
      </nav>
      <div class="topbar-right mono">
        <RouterLink to="/new" class="new-job" active-class="new-job--active">
          <span aria-hidden="true">+</span>
          <span class="new-job-label"><span class="new-job-verb">新建 </span>job</span>
        </RouterLink>
        <RouterLink to="/schedules/new" class="new-job" active-class="new-job--active">
          <span aria-hidden="true">+</span>
          <span class="new-job-label"><span class="new-job-verb">新建 </span>cron</span>
        </RouterLink>
        <EscalationBell />
        <span class="conn" :class="connected ? 'conn--on' : 'conn--off'">
          <span class="conn-dot"></span>
          <span class="conn-label">{{ connected ? 'connected' : 'offline' }}</span>
        </span>
        <TopbarMenu @logout="logout" />
      </div>
    </header>

    <div v-if="showChrome" class="shell">
      <aside class="drawer-nav" :class="{ 'drawer-nav--open': drawerOpen }" aria-label="移动端主导航">
        <nav class="drawer-nav-inner mono" aria-label="主导航抽屉">
          <RouterLink
            :to="homeNav.to"
            class="drawer-link"
            active-class="drawer-link--active"
            @click="closeDrawer"
          >
            {{ homeNav.label }}
          </RouterLink>

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
            </RouterLink>
          </section>

          <RouterLink
            :to="settingsNav.to"
            class="drawer-link drawer-link--settings"
            active-class="drawer-link--active"
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
.nav-settings {
  border-left: 1px solid var(--line);
  padding-left: 14px;
  margin-left: 2px;
}

.topbar-right {
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

/* 响应式：窄屏左轨折叠为抽屉 */
@media (max-width: 768px) {
  .rail-toggle {
    display: inline-block;
  }
  .topbar {
    gap: 10px;
    padding: 10px 12px;
  }
  .brand-sub,
  .brand-sep {
    display: none;
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
  .conn-label {
    display: none;
  }
  .new-job-verb {
    display: none;
  }
  .conn {
    gap: 0;
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
}
</style>
