<script setup lang="ts">
// 应用入口：只负责「壳内 / 壳外」与几个全局面（今天队列、决策浮层、撤销）。
// 顶栏、侧栏、标签栏、信号带都在 ui/AppShell.vue 里（设计稿 §6）。
import { computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from './ui/AppShell.vue'
import DecisionOverlay from './components/today/DecisionOverlay.vue'
import HandledDrawer from './components/today/HandledDrawer.vue'
import UndoToast from './components/today/UndoToast.vue'
import { installTodayGlobals, startToday, stopToday } from './store/today'
import { startWorkNeedsMe, stopWorkNeedsMe } from './store/workNeedsMe'
import { startUpdateAvailable, stopUpdateAvailable } from './store/updateAvailable'

const router = useRouter()
const route = useRoute()

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

// 连接态由 AppShell 按 token 与 api/live 的实时状态自己算，App 只管子壳与全局浮层。
</script>

<template>
  <AppShell v-if="showChrome">
    <RouterView />
  </AppShell>
  <main v-else class="content-bare">
    <RouterView />
  </main>
  <template v-if="showChrome">
    <DecisionOverlay />
    <HandledDrawer />
    <UndoToast />
  </template>
</template>

<style scoped>
.content-bare {
  min-height: 100vh;
  padding: var(--s4);
}
</style>
