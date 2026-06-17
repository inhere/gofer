<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { clearToken, hasToken } from './store/auth'

const router = useRouter()
const route = useRoute()

// 连接态：基于是否有 token 的简单标识（连通性探活留给具体页面/T6）
const connected = ref(false)

function refreshConn() {
  connected.value = hasToken()
}

onMounted(refreshConn)

// 是否展示顶栏：接入页不展示导航
const showChrome = computed(() => route.path !== '/access')

function logout() {
  clearToken()
  connected.value = false
  router.replace({ path: '/access' })
}

const navItems = [
  { to: '/board', label: 'board' },
  { to: '/projects', label: 'projects' },
  { to: '/agents', label: 'agents' },
]
</script>

<template>
  <div class="app-root">
    <header v-if="showChrome" class="topbar">
      <div class="brand mono">
        <span class="brand-name">AGENT-BRIDGE</span>
        <span class="brand-sep">&#9656;</span>
        <span class="brand-sub">console</span>
      </div>
      <nav class="nav mono">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          active-class="nav-link--active"
        >
          {{ item.label }}
        </RouterLink>
      </nav>
      <div class="topbar-right mono">
        <span class="conn" :class="connected ? 'conn--on' : 'conn--off'">
          <span class="conn-dot"></span>
          {{ connected ? 'connected' : 'offline' }}
        </span>
        <button class="logout" type="button" @click="logout">登出</button>
      </div>
    </header>
    <main class="content">
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
  gap: 16px;
  font-size: 13px;
}
.nav-link {
  color: var(--queue);
  padding: 2px 0;
  border-bottom: 1px solid transparent;
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

.content {
  flex: 1;
  padding: 18px;
}
</style>
