<script setup lang="ts">
// 收件箱（design docs/design/2026-10-28-inbox-triage-design.md §3.1 / §6a）：
// 两栏按「要不要你动手」划分——「要处理」（action）与「仅知会」（fyi），各自显示「未读 / 总数」。
//  - 宽屏两栏并排；手机（≤760）改成 ui/Tabs 页签切换（?tab=fyi 可深链，默认「要处理」）；
//  - 已读 / 清理只写个人状态（POST /v1/inbox/state），不执行源事项的任何动作；
//  - 「全部已读」按栏批量（带 column），「清理已读」只清本栏已看过的条目，未读不动；
//  - 数据与计数在 store/inbox.ts，随推送主题防抖重拉；侧栏徽标只算「要处理」的未读。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import type { InboxColumn, InboxItem } from '../api/inbox'
import Switch from '../ui/Switch.vue'
import StatusPill from '../ui/StatusPill.vue'
import Tabs from '../ui/Tabs.vue'
import Toast from '../ui/Toast.vue'
import { toastDuration } from '../ui/toast'
import type { TabItem } from '../ui/tabs'
import { t } from '../i18n'
import { includeExec } from '../store/today'
import {
  inboxBusy,
  inboxData,
  inboxError,
  inboxUnavailable,
  markInbox,
  refreshInbox,
  setInboxIncludeExec,
  setShowCleared,
  showCleared,
  startInbox,
} from '../store/inbox'
import { fmtAgo } from '../api/time'
import { INBOX_COLUMNS, emptyInbox, itemFamily, itemLink, kindLabelKey, seenKeys } from '../utils/inbox'

const data = computed(() => inboxData.value ?? emptyInbox())

const toast = ref<{ text: string; tone: 'neutral' | 'danger' } | null>(null)
const TOAST_FALLBACK_MS = 4000

function itemsOf(col: InboxColumn): InboxItem[] {
  return data.value.columns[col]
}

// 页签（仅手机可见）：值是栏位名，默认「要处理」；计数 = 未读数。
const tabs = computed<TabItem[]>(() =>
  INBOX_COLUMNS.map((col) => ({
    value: col,
    label: t(`inbox.col.${col}`),
    count: data.value.unread[col],
    ...(col === 'action' ? { badge: data.value.unread.action > 0 } : {}),
  })),
)

function showToast(text: string, tone: 'neutral' | 'danger' = 'neutral'): void {
  toast.value = { text, tone }
}

async function run(
  action: 'seen' | 'clear' | 'restore',
  opts: { keys?: string[]; column?: InboxColumn },
  doneKey: string,
): Promise<void> {
  if (inboxBusy.value) return
  try {
    const n = await markInbox(action, opts)
    showToast(t(doneKey, { n }))
  } catch (e) {
    showToast(t('inbox.toast.failed', { msg: e instanceof Error ? e.message : String(e) }), 'danger')
    void refreshInbox()
  }
}

const markOne = (it: InboxItem) => run('seen', { keys: [it.key] }, 'inbox.toast.seen')
const clearOne = (it: InboxItem) => run('clear', { keys: [it.key] }, 'inbox.toast.cleared')
const restoreOne = (it: InboxItem) => run('restore', { keys: [it.key] }, 'inbox.toast.restored')
const markAll = (col: InboxColumn) => run('seen', { column: col }, 'inbox.toast.seen')
function clearRead(col: InboxColumn): Promise<void> {
  const keys = seenKeys(itemsOf(col))
  if (keys.length === 0) return Promise.resolve()
  return run('clear', { keys, column: col }, 'inbox.toast.cleared')
}

const execOn = computed({
  get: () => includeExec.value,
  set: (v: boolean) => setInboxIncludeExec(v),
})
const clearedOn = computed({
  get: () => showCleared.value,
  set: (v: boolean) => setShowCleared(v),
})

onMounted(() => {
  startInbox()
  void refreshInbox()
})
// 订阅属于导航壳（App.vue 里启停），页面只负责进入时拉一次；离开不退订。
onUnmounted(() => {
  toast.value = null
})
</script>

<template>
  <div class="page inbox" data-test="inbox-page">
    <header class="inb-head">
      <div>
        <h1 class="title">{{ t('inbox.title') }}</h1>
        <p class="subtitle" data-test="inbox-subtitle">
          {{ t('inbox.subtitle', { n: data.total_unread }) }}
        </p>
      </div>
      <div class="inb-opts">
        <Switch v-model="execOn" data-test="inbox-exec">{{ t('inbox.opt.exec') }}</Switch>
        <Switch v-model="clearedOn" data-test="inbox-cleared">{{ t('inbox.opt.cleared') }}</Switch>
        <button class="inb-btn" type="button" data-test="inbox-refresh" @click="refreshInbox">
          {{ t('inbox.act.refresh') }}
        </button>
      </div>
    </header>

    <p v-if="inboxUnavailable" class="inb-note" data-test="inbox-unavailable">{{ t('inbox.unavailable') }}</p>
    <p v-else-if="inboxError" class="inb-error" data-test="inbox-error">{{ inboxError }}</p>

    <div class="inb-tabs" data-test="inbox-tabs">
      <Tabs :tabs="tabs" :label="t('inbox.title')">
        <template #default="{ active }">
          <div class="inb-board" :data-active="active">
            <section
              v-for="col in INBOX_COLUMNS"
              :key="col"
              class="inb-col"
              :class="{ 'inb-col--off': col !== active }"
              :data-column="col"
              :data-test="`inbox-col-${col}`"
            >
              <div class="inb-col-head">
                <h2 class="inb-col-title">{{ t(`inbox.col.${col}`) }}</h2>
                <span class="inb-col-count" :data-test="`inbox-count-${col}`">
                  {{ t('inbox.col.count', { unread: data.unread[col], total: itemsOf(col).length }) }}
                </span>
                <span class="inb-col-actions">
                  <button
                    class="inb-btn"
                    type="button"
                    :disabled="data.unread[col] === 0 || inboxBusy"
                    :data-test="`inbox-mark-all-${col}`"
                    @click="markAll(col)"
                  >
                    {{ t('inbox.act.markAll') }}
                  </button>
                  <button
                    class="inb-btn"
                    type="button"
                    :disabled="seenKeys(itemsOf(col)).length === 0 || inboxBusy"
                    :title="t('inbox.act.clearReadHint')"
                    :data-test="`inbox-clear-read-${col}`"
                    @click="clearRead(col)"
                  >
                    {{ t('inbox.act.clearRead') }}
                  </button>
                </span>
              </div>
              <p class="inb-col-hint">{{ t(`inbox.col.${col}.hint`) }}</p>

              <p v-if="itemsOf(col).length === 0" class="inb-empty" :data-test="`inbox-empty-${col}`">
                {{ t(`inbox.empty.${col}`) }}
              </p>
              <ul v-else class="inb-list">
                <li
                  v-for="it in itemsOf(col)"
                  :key="it.key"
                  class="inb-item"
                  :class="{ 'inb-item--unread': it.state === 'unread', 'inb-item--cleared': it.state === 'cleared' }"
                  :data-state="it.state"
                  data-test="inbox-item"
                >
                  <div class="inb-item-top">
                    <StatusPill :family="itemFamily(it)" :label="it.waiting_label || t(kindLabelKey(it.kind))" />
                    <span v-if="it.project_key" class="inb-proj mono">{{ it.project_key }}</span>
                    <span v-if="it.activity_at" class="inb-ago">{{ fmtAgo(it.activity_at) }}</span>
                  </div>
                  <div class="inb-title">{{ it.title }}</div>
                  <div v-if="it.summary" class="inb-summary">{{ it.summary }}</div>
                  <div class="inb-item-actions">
                    <RouterLink v-if="itemLink(it)" class="inb-btn" :to="itemLink(it)" data-test="inbox-open">
                      {{ t('inbox.act.open') }}
                    </RouterLink>
                    <button
                      v-if="it.state === 'unread'"
                      class="inb-btn"
                      type="button"
                      :disabled="inboxBusy"
                      data-test="inbox-mark"
                      @click="markOne(it)"
                    >
                      {{ t('inbox.act.markSeen') }}
                    </button>
                    <button
                      v-if="it.state !== 'cleared'"
                      class="inb-btn"
                      type="button"
                      :disabled="inboxBusy"
                      :title="t('inbox.act.clearHint')"
                      data-test="inbox-clear"
                      @click="clearOne(it)"
                    >
                      {{ t('inbox.act.clear') }}
                    </button>
                    <button
                      v-else
                      class="inb-btn"
                      type="button"
                      :disabled="inboxBusy"
                      data-test="inbox-restore"
                      @click="restoreOne(it)"
                    >
                      {{ t('inbox.act.restore') }}
                    </button>
                  </div>
                </li>
              </ul>
            </section>
          </div>
        </template>
      </Tabs>
    </div>

    <Toast
      v-if="toast"
      placement="bottom"
      closable
      :tone="toast.tone"
      :text="toast.text"
      :duration="toastDuration() || TOAST_FALLBACK_MS"
      @close="toast = null"
    />
  </div>
</template>

<style scoped>
.inbox {
  min-width: 0;
}
.inb-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--s3);
  flex-wrap: wrap;
  margin-bottom: var(--s3);
}
.title {
  font-size: var(--fs-xl);
  letter-spacing: -0.015em;
  color: var(--ink);
  margin: 0;
}
.subtitle {
  color: var(--ink-2);
  font-size: var(--fs-sm);
  margin: var(--s1) 0 0;
}
.inb-opts {
  display: flex;
  align-items: center;
  gap: var(--s3);
  flex-wrap: wrap;
}
.inb-error {
  color: var(--fail);
  font-size: var(--fs-sm);
}
.inb-note {
  color: var(--ink-2);
  font-size: var(--fs-sm);
}

/* 宽屏：页签隐藏，两栏并排；手机：页签显示，只显示当前栏。 */
.inb-tabs :deep(.tabs) {
  display: none;
}
.inb-tabs :deep(.tab-panel) {
  padding-top: 0;
}
.inb-board {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--s4);
  align-items: start;
}
.inb-col {
  min-width: 0;
}
.inb-col-head {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
}
.inb-col-title {
  margin: 0;
  font-size: var(--fs-md);
  color: var(--ink);
}
.inb-col-count {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  font-variant-numeric: tabular-nums;
}
.inb-col-actions {
  display: inline-flex;
  gap: var(--s1);
  margin-left: auto;
}
.inb-col-hint {
  margin: var(--s1) 0 var(--s2);
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.inb-empty {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  padding: var(--s3) 0;
}
.inb-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--s2);
}
.inb-item {
  min-width: 0;
  padding: var(--s2) var(--s3);
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
}
/* 黄色只代表「需要人」：未读条目用左侧强调线，不整卡着色。 */
.inb-item--unread {
  border-left: 3px solid var(--accent);
}
.inb-item--cleared {
  opacity: 0.6;
}
.inb-item-top {
  display: flex;
  align-items: center;
  gap: var(--s2);
  flex-wrap: wrap;
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.inb-title {
  margin-top: var(--s1);
  color: var(--ink);
  font-size: var(--fs-sm);
  overflow-wrap: anywhere;
}
.inb-summary {
  margin-top: 2px;
  color: var(--ink-2);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.inb-item-actions {
  display: flex;
  gap: var(--s1);
  flex-wrap: wrap;
  margin-top: var(--s2);
}
.inb-btn {
  display: inline-flex;
  align-items: center;
  min-height: 28px;
  padding: 0 var(--s2);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  color: var(--ink);
  font: inherit;
  font-size: var(--fs-xs);
  text-decoration: none;
  cursor: pointer;
}
.inb-btn:hover:not(:disabled) {
  border-color: var(--accent);
}
.inb-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.inb-btn:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}

@media (max-width: 760px) {
  .inb-tabs :deep(.tabs) {
    display: flex;
    position: sticky;
    top: 0;
    z-index: 5;
    background: var(--surface);
  }
  .inb-tabs :deep(.tab-panel) {
    padding-top: var(--s3);
  }
  .inb-board {
    grid-template-columns: minmax(0, 1fr);
  }
  .inb-col--off {
    display: none;
  }
  .inb-btn {
    min-height: 40px;
  }
}
</style>
