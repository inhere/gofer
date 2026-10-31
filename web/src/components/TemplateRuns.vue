<script setup lang="ts">
// 某个工作流模板最近的执行记录（工作流详情页内嵌）：listWorkflows({template}) 的前 10 条，
// 完整分页 / 状态筛选在 /workflows/runs?template=。
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { listWorkflows } from '../api/client'
import { fmtDateTime } from '../api/time'
import { t } from '../i18n'
import StatusPill from '../ui/StatusPill.vue'
import { createLiveTopic } from '../utils/useLiveTopic'
import { runsLink } from '../utils/workflowList'
import type { Workflow } from '../api/types'

const props = defineProps<{ template: string }>()

const RECENT = 10

const runs = ref<Workflow[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref('')

async function fetchRuns(): Promise<void> {
  loading.value = true
  try {
    const resp = await listWorkflows({ template: props.template, limit: RECENT })
    runs.value = resp.workflows ?? []
    total.value = resp.total ?? runs.value.length
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.template, () => void fetchRuns())

const live = createLiveTopic('workflows', { initial: false, fetch: fetchRuns })

onMounted(() => {
  void fetchRuns()
  live.start()
})
onUnmounted(() => live.stop())
</script>

<template>
  <section class="runs" data-test="template-runs">
    <p v-if="error" class="error">{{ error }}</p>
    <ul v-if="runs.length" class="list">
      <li v-for="wf in runs" :key="wf.id" class="row" data-test="template-run-row">
        <StatusPill :status="wf.status" />
        <RouterLink class="row-id mono" :to="`/workflows/${encodeURIComponent(wf.id)}`">{{ wf.title || wf.id }}</RouterLink>
        <span class="row-time dim">{{ fmtDateTime(wf.created_at) }}</span>
      </li>
    </ul>
    <p v-else-if="!error && !loading" class="dim" data-test="template-runs-empty">{{ t('workflows.list.neverRun') }}</p>
    <RouterLink v-if="total > runs.length" class="more" :to="runsLink(template)" data-test="template-runs-all">
      {{ t('workflows.tpl.allRuns', { n: total }) }}
    </RouterLink>
  </section>
</template>

<style scoped>
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  background: var(--surface);
}
.row {
  display: flex;
  align-items: center;
  gap: var(--s3);
  padding: 8px var(--s3);
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-sm);
  min-width: 0;
}
.row:last-child {
  border-bottom: none;
}
.row-id {
  flex: 1;
  min-width: 0;
  color: var(--accent);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row-time,
.dim {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  white-space: nowrap;
}
.more {
  display: inline-block;
  margin-top: var(--s2);
  font-size: var(--fs-sm);
}
.error {
  color: var(--fail);
  font-size: var(--fs-sm);
}
</style>
