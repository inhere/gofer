<script setup lang="ts">
// 设置 → 审计（设计 2026-11-14 §1）：管理类（写）请求的留痕，谁、何时、调了哪条路由、对哪个资源、
// 结果如何。只读——审计行是服务端中间件写的，这里不提供任何修改入口。
//
// 权限是 audit.read（默认只有根管理员，可授予）；403 时整页换成「需要 audit.read 权限」，
// 不把「没权限」显示成「没有记录」。分页走 cursor：next_cursor 取上一页最后一行的 id。
import { computed, onMounted, reactive, ref } from 'vue'
import { EMPTY_AUDIT_FILTER, listAdminAudit } from '../../api/adminAudit'
import type { AdminAuditFilter } from '../../api/adminAudit'
import { ApiError } from '../../api/client'
import { fmtDateTime } from '../../api/time'
import type { AdminAuditEntry } from '../../api/types'
import { t } from '../../i18n'
import FormSection from '../../ui/FormSection.vue'

const filter = reactive<AdminAuditFilter>({ ...EMPTY_AUDIT_FILTER })
const entries = ref<AdminAuditEntry[]>([])
const nextCursor = ref(0)
const loading = ref(false)
const loaded = ref(false)
const denied = ref(false)
const error = ref('')

const hasMore = computed(() => nextCursor.value > 0)

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

async function fetchPage(cursor = 0): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const r = await listAdminAudit(filter, cursor)
    const page = r.entries ?? []
    entries.value = cursor > 0 ? [...entries.value, ...page] : page
    nextCursor.value = r.next_cursor ?? 0
    denied.value = false
    loaded.value = true
  } catch (e) {
    if (e instanceof ApiError && e.status === 403) {
      denied.value = true
      entries.value = []
      nextCursor.value = 0
      return
    }
    error.value = msg(e)
  } finally {
    loading.value = false
  }
}

// 任何筛选变化都从第一页重来：游标只对同一组筛选有意义。
function apply(): void {
  void fetchPage(0)
}

function reset(): void {
  Object.assign(filter, EMPTY_AUDIT_FILTER)
  apply()
}

onMounted(() => {
  void fetchPage(0)
})

function statusText(e: AdminAuditEntry): string {
  return e.status > 0 ? String(e.status) : '-'
}

function statusTone(e: AdminAuditEntry): string {
  if (e.status === 0) return 'idle'
  return e.status < 400 ? 'done' : 'fail'
}

function callerText(e: AdminAuditEntry): string {
  if (!e.caller_id) return '-'
  return e.caller_kind ? t('settings.audit.caller', { id: e.caller_id, kind: e.caller_kind }) : e.caller_id
}

function durationText(ms: number): string {
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms} ms`
}
</script>

<template>
  <div class="audit-page">
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.audit.title') }}</h1>
        <p class="sub">{{ t('settings.audit.sub') }}</p>
      </div>
      <button class="btn" type="button" :disabled="loading || denied" @click="apply()">
        {{ loading ? t('settings.audit.refreshing') : t('act.refresh') }}
      </button>
    </header>

    <p v-if="denied" class="msg msg--err" data-testid="audit-denied">{{ t('settings.audit.needsPermission') }}</p>

    <template v-else>
      <p v-if="error" class="msg msg--err" :title="error">{{ t('settings.audit.failed', { error }) }}</p>

      <FormSection :title="t('settings.audit.filter.title')" :desc="t('settings.audit.filter.desc')">
        <div class="filters">
          <label class="field">
            <span>{{ t('settings.audit.field.caller') }}</span>
            <input v-model="filter.caller" class="mono" data-testid="audit-filter-caller" @keyup.enter="apply()" />
          </label>
          <label class="field">
            <span>{{ t('settings.audit.field.route') }}</span>
            <input v-model="filter.route" class="mono" data-testid="audit-filter-route" @keyup.enter="apply()" />
          </label>
          <label class="field">
            <span>{{ t('settings.audit.field.status') }}</span>
            <select v-model="filter.status" class="mono" data-testid="audit-filter-status" @change="apply()">
              <option value="">{{ t('act.all') }}</option>
              <option value="ok">{{ t('settings.audit.status.ok') }}</option>
              <option value="fail">{{ t('settings.audit.status.fail') }}</option>
            </select>
          </label>
          <label class="field">
            <span>{{ t('settings.audit.field.since') }}</span>
            <input v-model="filter.since" class="mono" placeholder="2026-11-01" data-testid="audit-filter-since" @keyup.enter="apply()" />
          </label>
          <label class="field">
            <span>{{ t('settings.audit.field.until') }}</span>
            <input v-model="filter.until" class="mono" placeholder="2026-11-30" data-testid="audit-filter-until" @keyup.enter="apply()" />
          </label>
        </div>
        <p class="hint">{{ t('settings.audit.filter.hint') }}</p>
        <div class="row-actions">
          <button class="btn" type="button" data-testid="audit-filter-apply" @click="apply()">
            {{ t('act.filter') }}
          </button>
          <button class="btn" type="button" @click="reset()">{{ t('settings.audit.filter.reset') }}</button>
        </div>
      </FormSection>

      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('settings.audit.col.at') }}</th>
              <th>{{ t('settings.audit.col.caller') }}</th>
              <th>{{ t('settings.audit.col.route') }}</th>
              <th>{{ t('settings.audit.col.target') }}</th>
              <th>{{ t('settings.audit.col.status') }}</th>
              <th>{{ t('settings.audit.col.duration') }}</th>
              <th>{{ t('settings.audit.col.ip') }}</th>
              <th>{{ t('settings.audit.col.error') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in entries" :key="e.id" :data-audit-id="e.id">
              <td class="mono">{{ fmtDateTime(e.at) }}</td>
              <td class="mono">{{ callerText(e) }}</td>
              <td class="mono">
                <span class="method">{{ e.method }}</span> {{ e.route }}
              </td>
              <td class="mono">{{ e.target || '-' }}</td>
              <td>
                <span class="tag" :class="`tag--${statusTone(e)}`">{{ statusText(e) }}</span>
              </td>
              <td class="mono">{{ durationText(e.duration_ms) }}</td>
              <td class="mono">{{ e.ip || '-' }}</td>
              <td class="err" :title="e.error">{{ e.error || '-' }}</td>
            </tr>
            <tr v-if="loaded && entries.length === 0">
              <td colspan="8">{{ t('settings.audit.empty') }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-if="hasMore" class="row-actions">
        <button class="btn" type="button" :disabled="loading" data-testid="audit-more" @click="fetchPage(nextCursor)">
          {{ loading ? t('settings.audit.refreshing') : t('settings.audit.more') }}
        </button>
      </p>
    </template>
  </div>
</template>

<style scoped>
.audit-page {
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
.sub,
.hint {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
  line-height: 1.6;
}
.filters {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 10px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--fs-sm);
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
.method {
  color: var(--ink-3);
}
.err {
  max-width: 280px;
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
.tag--done {
  color: var(--accent);
  border-color: var(--accent);
}
.tag--fail {
  color: var(--fail);
  border-color: var(--fail);
}
.tag--idle {
  color: var(--ink-3);
}
.row-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
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
  word-break: break-word;
}
@media (max-width: 900px) {
  .table-wrap .table {
    min-width: 900px;
  }
  .page-head {
    flex-direction: column;
  }
}
</style>
