<script setup lang="ts">
// 常驻 agent（设计 2026-10-22 mail-agent-address「D6」）：server 配置里能收信的名字（agent:<名字>），
// 以及当前为它们跑着的常驻会话（名字、项目、job、信件数、空闲、结束按钮）。独立组件，Mail.vue 只
// 挂载它，写信框与布局的变化与本组件无关。
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { endMailAgent, listMailAgents } from '../../api/client'
import type { MailAgentsResp, MailResidentSession } from '../../api/types'
import { fmtAgo } from '../../api/time'
import { t } from '../../i18n'

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
      <h2 class="resident-title">{{ t('mail.resident.title') }}</h2>
      <span class="dim" data-test="mail-resident-cap">
        {{ t('mail.resident.cap', { n: live.length, max: data.agents_max_resident }) }}
      </span>
      <button type="button" class="tiny" :disabled="loading" data-test="mail-resident-refresh" @click="load">
        {{ t('mail.resident.refresh') }}
      </button>
    </header>
    <p v-if="error" class="err" data-test="mail-resident-error">{{ error }}</p>
    <p v-if="!data.agents.length" class="dim" data-test="mail-resident-empty">
      {{ t('mail.resident.empty') }}
    </p>
    <template v-else>
      <div class="chips" data-test="mail-resident-names">
        <code v-for="n in names" :key="n" class="chip">{{ n }}</code>
      </div>
      <!-- 左栏只有 320–340px：每行两个层——名字 / 状态 / 结束在上，job、信件数、空闲在下。 -->
      <div v-if="data.residents.length" class="rows" data-test="mail-resident-table">
        <div v-for="r in data.residents" :key="`${r.name}@${r.project ?? ''}`" class="row" data-test="mail-resident-row">
          <div class="row-top">
            <span class="mono name">{{ r.name }}</span>
            <span class="mono dim ellipsis">{{ r.project || '—' }}</span>
            <span class="mono dim" :title="r.last_error || r.status">{{ r.status }}</span>
            <button
              type="button"
              class="tiny"
              :disabled="ending === `${r.name}@${r.project ?? ''}` || r.status !== 'resident'"
              data-test="mail-resident-end"
              @click="end(r)"
            >
              {{ t('mail.resident.end') }}
            </button>
          </div>
          <div class="row-meta mono dim">
            <RouterLink v-if="r.job_id" :to="`/jobs/${encodeURIComponent(r.job_id)}`" data-test="mail-resident-job">
              {{ r.job_id }}
            </RouterLink>
            <span v-else>—</span>
            <span>{{ r.letters }}<template v-if="r.pending">{{ t('mail.resident.pending', { n: r.pending }) }}</template></span>
            <span v-if="idleOf(r)">{{ idleOf(r) }}</span>
          </div>
        </div>
      </div>
      <p v-else class="dim" data-test="mail-resident-none">{{ t('mail.resident.none') }}</p>
    </template>
  </section>
</template>

<style scoped>
.resident {
  border-top: 1px solid var(--line);
  padding: var(--s2) var(--s3);
  /* 信箱页是满高 flex 布局，常驻 agent 在左栏底部：按内容高度，长了内部自己滚。 */
  flex: 0 0 auto;
  max-height: 30vh;
  overflow: auto;
}
.resident-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.resident-title {
  font-size: var(--fs-sm);
  margin: 0;
  color: var(--fg);
}
.dim {
  color: var(--fg-3);
  font-size: var(--fs-xs);
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
  border-radius: var(--r-chip);
  padding: 1px 6px;
}
.rows {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
}
.row {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 4px 0;
  border-top: 1px solid var(--line);
}
.row-top {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: var(--fs-xs);
}
.row-top .name {
  color: var(--fg);
  font-weight: 600;
}
.row-top .tiny {
  margin-left: auto;
}
.row-meta {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
  font-size: 11px;
}
.ellipsis {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 12ch;
}
.mono {
  font-family: var(--font-mono);
}
.tiny {
  font-size: 11px;
  background: none;
  border: 1px solid var(--line-strong);
  border-radius: var(--r-chip);
  padding: 1px 6px;
  color: var(--fg);
  cursor: pointer;
}
.tiny:disabled {
  opacity: 0.5;
  cursor: default;
}
</style>
