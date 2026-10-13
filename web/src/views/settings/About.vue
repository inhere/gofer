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
import { checkUpdate, getStats, getUpdateStatus } from '../../api/client'
import { fmtAgo, fmtDateTime, fmtDuration } from '../../api/time'
import type { Stats, UpdateStatus } from '../../api/types'

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

// 检查更新（gofer-6x2n）：server 每隔一段时间（默认 2h）自己检查一次，这里读它最近的结果；
// 「检查更新」立即查一次。只检查不升级——升级仍用 gofer serve upgrade / worker upgrade。
const update = ref<UpdateStatus | null>(null)
const updateError = ref('')
const checking = ref(false)
const sourceLabel = computed(() => (update.value?.source === 'mirror' ? '内部 mirror' : 'GitHub'))
const checkedAt = computed(() => {
  const at = update.value?.checked_at
  return at ? `${fmtDateTime(at)}（${fmtAgo(at)}）` : '还没检查过'
})

async function loadUpdate(): Promise<void> {
  updateError.value = ''
  try {
    update.value = await getUpdateStatus()
  } catch (e) {
    updateError.value = e instanceof Error ? e.message : String(e)
  }
}

async function runCheck(): Promise<void> {
  checking.value = true
  updateError.value = ''
  try {
    update.value = await checkUpdate()
  } catch (e) {
    updateError.value = e instanceof Error ? e.message : String(e)
  } finally {
    checking.value = false
  }
}

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
  void loadUpdate()
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

    <div class="facts update">
      <div class="facts-head">
        <h2 class="facts-title mono">更新 / UPDATE</h2>
        <button class="mini-btn mono" type="button" :disabled="checking" @click="runCheck()">
          {{ checking ? '检查中...' : '检查更新' }}
        </button>
      </div>

      <p v-if="updateError" class="error mono" :title="updateError">{{ updateError }}</p>

      <dl v-if="update" class="facts-grid mono">
        <dt>最新版本</dt>
        <dd class="brk">
          <template v-if="update.latest">
            {{ update.latest }}
            <span v-if="update.newer" class="badge badge--new">有新版本</span>
            <span v-else-if="!update.error" class="badge">已是最新</span>
          </template>
          <template v-else>—</template>
        </dd>

        <template v-if="update.released_at">
          <dt>发布时间</dt>
          <dd class="brk">{{ update.released_at }}</dd>
        </template>

        <template v-if="update.description">
          <dt>说明</dt>
          <dd class="brk">{{ update.description }}</dd>
        </template>

        <template v-if="update.download_url">
          <dt>本平台下载</dt>
          <dd class="brk"><a :href="update.download_url" target="_blank" rel="noopener">{{ update.download_url }}</a></dd>
        </template>

        <template v-if="update.release_url">
          <dt>发布页</dt>
          <dd class="brk"><a :href="update.release_url" target="_blank" rel="noopener">{{ update.release_url }}</a></dd>
        </template>

        <dt>来源</dt>
        <dd>{{ sourceLabel }}</dd>

        <dt>上次检查</dt>
        <dd>{{ checkedAt }}</dd>

        <template v-if="update.error">
          <dt>检查失败</dt>
          <dd class="brk fail">{{ update.error }}</dd>
        </template>
      </dl>

      <p class="hint mono">
        server 会定时检查（默认每 2 小时，配置 <code>server.update</code>，可改用内部 mirror）。这里只检查不升级，
        升级用 <code>gofer serve upgrade</code> / <code>gofer worker upgrade</code>。
      </p>
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
.update {
  margin-top: 14px;
}
.badge {
  display: inline-block;
  margin-left: 8px;
  padding: 0 6px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 11px;
}
.badge--new {
  border-color: var(--done);
  color: var(--done);
}
.fail {
  color: var(--fail);
}
.hint {
  color: var(--queue);
  font-size: 11px;
  margin: 12px 0 0;
  line-height: 1.6;
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
