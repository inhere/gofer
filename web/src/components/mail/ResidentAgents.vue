<script setup lang="ts">
// 常驻 agent（设计 2026-10-22 mail-agent-address「D6」）：server 配置里能收信的名字（agent:<名字>），
// 以及当前为它们跑着的常驻会话（名字、项目、job、信件数、空闲、结束按钮）。独立组件，Mail.vue 只
// 挂载它，写信框与布局的变化与本组件无关。
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { endMailAgent, listMailAgents } from '../../api/client'
import type { MailAgentsResp, MailResidentSession } from '../../api/types'
import { fmtAgo } from '../../api/time'

const data = ref<MailAgentsResp>({ agents_max_resident: 0, agents: [], residents: [] })
const loading = ref(false)
const error = ref('')
const ending = ref('')

const live = computed(() => data.value.residents.filter((r) => r.status === 'resident'))
const names = computed(() => data.value.agents.map((a) => `agent:${a.name}`))

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    data.value = await listMailAgents()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function end(r: MailResidentSession): Promise<void> {
  const key = `${r.name}@${r.project ?? ''}`
  ending.value = key
  error.value = ''
  try {
    await endMailAgent(r.name, r.project ?? '')
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    ending.value = ''
  }
}

function idleOf(r: MailResidentSession): string {
  return r.idle_since ? fmtAgo(r.idle_since) : ''
}

onMounted(load)
defineExpose({ load })
</script>

<template>
  <section class="resident" data-test="mail-resident-agents">
    <header class="resident-head">
      <h2 class="resident-title">常驻 agent</h2>
      <span class="dim" data-test="mail-resident-cap">
        {{ live.length }} / {{ data.agents_max_resident }} 个会话
      </span>
      <button type="button" class="tiny" :disabled="loading" data-test="mail-resident-refresh" @click="load">刷新</button>
    </header>
    <p v-if="error" class="err" data-test="mail-resident-error">{{ error }}</p>
    <p v-if="!data.agents.length" class="dim" data-test="mail-resident-empty">
      还没有配置收件 agent：在 server 配置的 mail.agents 里声明后，就能用 agent:&lt;名字&gt; 发信。
    </p>
    <template v-else>
      <div class="chips" data-test="mail-resident-names">
        <code v-for="n in names" :key="n" class="chip">{{ n }}</code>
      </div>
      <table v-if="data.residents.length" class="tbl" data-test="mail-resident-table">
        <thead>
          <tr>
            <th>名字</th>
            <th>项目</th>
            <th>会话 job</th>
            <th>信件</th>
            <th>空闲</th>
            <th>状态</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in data.residents" :key="`${r.name}@${r.project ?? ''}`" data-test="mail-resident-row">
            <td class="mono">{{ r.name }}</td>
            <td class="mono dim">{{ r.project || '—' }}</td>
            <td class="mono">
              <RouterLink v-if="r.job_id" :to="`/jobs/${encodeURIComponent(r.job_id)}`" data-test="mail-resident-job">
                {{ r.job_id }}
              </RouterLink>
              <span v-else class="dim">—</span>
            </td>
            <td class="mono">{{ r.letters }}<span v-if="r.pending" class="dim"> (+{{ r.pending }} 待投递)</span></td>
            <td class="mono dim">{{ idleOf(r) }}</td>
            <td class="mono" :title="r.last_error || undefined">{{ r.status }}</td>
            <td>
              <button
                type="button"
                class="tiny"
                :disabled="ending === `${r.name}@${r.project ?? ''}` || r.status !== 'resident'"
                data-test="mail-resident-end"
                @click="end(r)"
              >
                结束
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="dim" data-test="mail-resident-none">当前没有常驻会话（下一封 agent: 信件会启动一个）。</p>
    </template>
  </section>
</template>

<style scoped>
.resident {
  border: 1px solid var(--line);
  border-radius: 6px;
  padding: 10px 12px;
  margin-bottom: 12px;
  /* 信箱页是满高 flex 布局：这块按内容高度但不超过 30vh，长了内部自己滚，不挤对话区。 */
  flex: 0 0 auto;
  max-height: 30vh;
  overflow-y: auto;
}
.resident-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.resident-title {
  font-size: 13px;
  margin: 0;
}
.dim {
  color: var(--queue);
  font-size: 12px;
}
.err {
  color: var(--fail);
  font-size: 12px;
  margin: 4px 0;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
}
.chip {
  font-size: 11px;
  border: 1px solid var(--line);
  border-radius: 4px;
  padding: 1px 6px;
}
.tbl {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.tbl th,
.tbl td {
  text-align: left;
  padding: 3px 6px;
  border-bottom: 1px solid var(--line);
}
.mono {
  font-family: var(--font-mono);
}
.tiny {
  font-size: 11px;
  background: none;
  border: 1px solid var(--line);
  border-radius: 4px;
  padding: 1px 6px;
  color: var(--paper);
  cursor: pointer;
}
.tiny:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>
