<script setup lang="ts">
// WEB-12 关于页：server 的版本 / 地址 / 运行时长等自述信息，全部来自 GET /v1/stats。
//
// 只显示服务端**确实有**的字段（设计 §一：没有的就不显示那一行，不为了这一页加后端）：
//  - version：buildinfo.DisplayVersion()，构建时由 LDFLAGS 注入的 "vX.Y.Z (commit7)"，
//    所以版本与 commit 是同一行（没注入 commit 时就是纯版本号）。
//  - uptime_sec / server_time：进程运行时长与服务器当前时间。
//  - 服务器地址：控制台由 server 自己服务（请求走同源相对路径），当前页面的 origin 就是
//    控制台正在对话的那个地址。
//  - 构建时间 / worker wire 协议版本：/v1/stats 没有这两个字段，故不渲染（本期不改后端）。
import { computed, onMounted, ref } from 'vue'
import { getStats } from '../../api/client'
import { fmtDateTime, fmtDuration } from '../../api/time'
import type { Stats } from '../../api/types'

const stats = ref<Stats | null>(null)
const error = ref('')
const loading = ref(false)
const loaded = ref(false)

const serverAddr = window.location.origin

const version = computed(() => stats.value?.version || '未知（未注入构建版本）')
const uptime = computed(() => {
  const sec = stats.value?.uptime_sec
  return sec && sec > 0 ? fmtDuration(sec) : '—'
})
// server_time 是 Unix 毫秒（stats_handler.nowMillis），fmtDateTime 收秒。
const serverTime = computed(() => {
  const ms = stats.value?.server_time
  return ms ? fmtDateTime(Math.floor(ms / 1000)) : '—'
})

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    stats.value = await getStats()
    loaded.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="about">
    <p v-if="error" class="error mono" :title="error">读取服务端信息失败：{{ error }}</p>

    <div class="facts">
      <div class="facts-head">
        <h2 class="facts-title mono">服务端 / SERVER</h2>
        <button class="mini-btn mono" type="button" :disabled="loading" @click="load()">
          {{ loading ? '刷新中...' : '刷新' }}
        </button>
      </div>

      <dl class="facts-grid mono">
        <dt>版本</dt>
        <dd class="brk">{{ version }}</dd>

        <dt>服务器地址</dt>
        <dd class="brk">{{ serverAddr }}</dd>

        <dt>运行时长</dt>
        <dd>{{ uptime }}</dd>

        <dt>服务器时间</dt>
        <dd>{{ serverTime }}</dd>
      </dl>

      <p v-if="!loaded && !error" class="placeholder mono">读取中...</p>
    </div>
  </div>
</template>

<style scoped>
.about {
  min-width: 0;
}
.facts {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px 14px;
}
.facts-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.facts-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0;
}
.facts-grid {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  gap: 8px 14px;
  margin: 0;
  font-size: 12px;
}
.facts-grid dt {
  color: var(--queue);
}
.facts-grid dd {
  margin: 0;
  color: var(--paper);
}
.brk {
  word-break: break-all;
}
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}
.placeholder {
  color: var(--queue);
  font-size: 12px;
  margin: 10px 0 0;
}
</style>
