<script setup lang="ts">
// 设置 → 自检（设计 2026-11-14 §5）：server 每 60 秒跑一组检查，每项连续失败达到阈值才算故障，
// 一次成功即恢复。这里只读——检查本身由服务端循环做。
//
// 数据来自 GET /v1/health/checks；「等我」里的 server.health 卡片的「看详情」也落到这一页。
// 检查名是稳定标识（storage / disk / sweep / notification），显示文案按 i18n key 取，未知的
// 名字原样显示（服务端后加的检查项不会变成空白）。
import { computed, onMounted, ref } from 'vue'
import { getHealthChecks } from '../../api/health'
import { fmtDateTime } from '../../api/time'
import type { HealthCheck } from '../../api/types'
import { t, te } from '../../i18n'
import FormSection from '../../ui/FormSection.vue'

const checks = ref<HealthCheck[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref('')

const failing = computed(() => checks.value.filter((c) => c.failing))
const degraded = computed(() => failing.value.length > 0)

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    checks.value = (await getHealthChecks()).checks ?? []
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

function nameText(name: string): string {
  const key = `settings.health.check.${name}`
  return te(key) ? t(key) : name
}
</script>

<template>
  <div class="health-page">
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.health.title') }}</h1>
        <p class="sub">{{ t('settings.health.sub') }}</p>
      </div>
      <button class="btn" type="button" :disabled="loading" data-testid="health-refresh" @click="load()">
        {{ loading ? t('settings.health.refreshing') : t('act.refresh') }}
      </button>
    </header>

    <p v-if="error" class="msg msg--err" :title="error">{{ t('settings.health.failed', { error }) }}</p>

    <FormSection :title="t('settings.health.section')" :desc="t('settings.health.sectionDesc')">
      <p v-if="loaded && checks.length === 0" class="msg" data-testid="health-empty">{{ t('settings.health.empty') }}</p>
      <p v-else-if="degraded" class="msg msg--err" data-testid="health-degraded">
        {{ t('settings.health.degraded', { n: failing.length }) }}
      </p>
      <p v-else-if="loaded" class="msg msg--ok" data-testid="health-ok">{{ t('settings.health.allOk') }}</p>

      <div v-if="checks.length" class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('settings.health.col.name') }}</th>
              <th>{{ t('settings.health.col.state') }}</th>
              <th>{{ t('settings.health.col.failures') }}</th>
              <th>{{ t('settings.health.col.checked') }}</th>
              <th>{{ t('settings.health.col.error') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in checks" :key="c.name" :data-check="c.name">
              <td>{{ nameText(c.name) }}</td>
              <td>
                <span class="tag" :class="c.failing ? 'tag--off' : c.ok ? 'tag--ok' : 'tag--warn'">
                  {{ c.failing ? t('settings.health.state.failing') : c.ok ? t('settings.health.state.ok') : t('settings.health.state.flaky') }}
                </span>
              </td>
              <td class="mono">{{ c.consecutive_failures }}</td>
              <td class="mono">{{ c.checked_at ? fmtDateTime(c.checked_at) : '-' }}</td>
              <td class="err" :title="c.last_error">{{ c.last_error || '-' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </FormSection>
  </div>
</template>

<style scoped>
.health-page {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.page-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}
.page-head .grow {
  flex: 1;
  min-width: 0;
}
.page-head h1 {
  margin: 0 0 4px;
  font-size: var(--fs-xl);
  color: var(--ink);
}
.sub {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
}
.table-wrap {
  overflow-x: auto;
}
.table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--fs-sm);
}
.table th,
.table td {
  text-align: left;
  padding: 6px 8px;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}
.table th {
  color: var(--ink-3);
  font-weight: 500;
  white-space: nowrap;
}
.err {
  max-width: 320px;
  color: var(--fail);
  word-break: break-word;
}
.tag {
  font-size: var(--fs-xs);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 6px;
  white-space: nowrap;
}
.tag--ok {
  color: var(--accent);
  border-color: var(--accent);
}
.tag--off {
  color: var(--fail);
  border-color: var(--fail);
}
.tag--warn {
  color: var(--need);
  border-color: var(--need);
}
.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 6px 12px;
  font: inherit;
  font-size: var(--fs-sm);
  background: transparent;
  color: var(--ink);
}
.btn:hover:not(:disabled) {
  border-color: var(--accent);
  color: var(--accent);
}
.btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.msg {
  margin: 0;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 10px 12px;
  font-size: var(--fs-sm);
  color: var(--ink-2);
}
.msg--err {
  color: var(--fail);
  border-color: var(--fail);
}
.msg--ok {
  color: var(--accent);
  border-color: var(--accent);
}
</style>
