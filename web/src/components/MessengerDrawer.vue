<script setup lang="ts">
// 传话人抽屉：一个 runner 上常驻的 Claude SendMessage 进程的状态页。
//  - 状态 / 启动时间 / 最近使用 / 空闲退出倒计时（local 读 server 进程内快照，worker 读心跳）
//  - 最近 20 次投递摘要（传话 / 列出会话，成功或失败原因；消息只显示截断摘要）
//  - 子进程 stderr 尾部（4KB），排查“为什么传话失败”
//  - “列出可见会话”：问传话人 ListAgents，并与 gofer 登记的会话对照，行内一键去传话
import { runnerLabel } from '../utils/runnerDisplay'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listAgentSessions, listMessengerAgents } from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import { t } from '../i18n'
import type { AgentSession, MessengerAgentsResp, Runner } from '../api/types'
import {
  crossReference,
  formatAge,
  idleLeftText,
  messageBlockReason,
  messengerDisplay,
  messengerListBlock,
  opLabel,
  rotationLimitText,
  rotationReasonLabel,
  usageText,
} from '../utils/messenger'

const props = defineProps<{ runner: Runner; nowMs: number }>()
const emit = defineEmits<{ (e: 'close'): void }>()
const router = useRouter()

const listing = ref<MessengerAgentsResp | null>(null)
const registered = ref<AgentSession[]>([])
const listLoading = ref(false)
const listError = ref('')
const rawOpen = ref(false)

const nowSec = computed(() => Math.floor(props.nowMs / 1000))
const display = computed(() => messengerDisplay(props.runner))
const snap = computed(() => props.runner.messenger_detail)
const listBlock = computed(() => messengerListBlock(props.runner))
const rows = computed(() => (listing.value ? crossReference(listing.value.agents, registered.value) : []))
const deliveries = computed(() => snap.value?.deliveries ?? [])
const rotations = computed(() => snap.value?.rotations ?? [])
const ageText = computed(() => formatAge(snap.value?.age_sec))
const isWorker = computed(() => props.runner.type === 'worker')

async function loadAgents(refresh: boolean): Promise<void> {
  if (listLoading.value || listBlock.value) return
  listLoading.value = true
  listError.value = ''
  try {
    const [resp, sessions] = await Promise.all([
      listMessengerAgents(props.runner.name, refresh),
      listAgentSessions({ all: true, limit: 200 }).catch(() => null),
    ])
    listing.value = resp
    registered.value = sessions?.sessions ?? []
  } catch (e) {
    // 服务端的 detail 已经是中文原因（worker 太旧 / 不在线 / 模型调用失败）
    listError.value = (e as { detail?: string }).detail || (e instanceof Error ? e.message : String(e))
  } finally {
    listLoading.value = false
  }
}

function talkTo(sid: string): void {
  void router.push({ path: '/sessions', query: { sid } })
  emit('close')
}

function onEsc(ev: KeyboardEvent): void {
  if (ev.key === 'Escape') emit('close')
}

onMounted(() => document.addEventListener('keydown', onEsc))
onUnmounted(() => document.removeEventListener('keydown', onEsc))
</script>

<template>
  <div class="mdrawer-overlay" @click.self="emit('close')">
    <div class="mdrawer-panel" role="dialog" :aria-label="t('runners.messenger.title')" data-test="messenger-drawer">
      <header class="mdrawer-head">
        <div class="mdrawer-title mono">
          {{ t('runners.messenger.heading', { name: runnerLabel(runner.name) }) }}
          <span class="mstatus mono" :class="`mtone--${display.tone}`" data-test="drawer-status">{{ display.text }}</span>
        </div>
        <button class="act mono" type="button" @click="emit('close')">{{ t('runners.messenger.close') }}</button>
      </header>

      <div class="mdrawer-body">
        <section class="msec">
          <h3 class="msec-title mono">{{ t('runners.messenger.status') }}</h3>
          <p v-if="!display.known && isWorker && runner.status === 'connected'" class="mnote mono">
            {{ t('runners.messenger.notReported') }}
          </p>
          <dl v-else-if="snap" class="mdl mono">
            <dt>{{ t('runners.messenger.status') }}</dt><dd>{{ display.text }}<span v-if="idleLeftText(snap, nowSec)" class="dim"> · {{ idleLeftText(snap, nowSec) }}</span></dd>
            <dt>{{ t('runners.messenger.startedAt') }}</dt><dd>{{ snap.started_at ? t('runners.messenger.atAgo', { at: fmtDateTime(snap.started_at), ago: fmtAgo(snap.started_at, nowSec) }) : t('runners.messenger.notStarted') }}</dd>
            <dt>{{ t('runners.messenger.lastUsed') }}</dt><dd>{{ snap.last_used_at ? t('runners.messenger.atAgo', { at: fmtDateTime(snap.last_used_at), ago: fmtAgo(snap.last_used_at, nowSec) }) : t('runners.messenger.neverUsed') }}</dd>
            <dt>{{ t('runners.messenger.handled') }}</dt>
            <dd data-test="messenger-counters">
              {{ t('runners.messenger.messageCount', { n: snap.messages ?? 0 }) }}<template v-if="ageText"> · {{ t('runners.messenger.aliveFor', { age: ageText }) }}</template>
              <span class="dim"> · {{ rotationLimitText(snap) }}</span>
            </dd>
            <dt>{{ t('runners.messenger.usage') }}</dt>
            <dd data-test="messenger-usage">{{ usageText(snap.usage) || t('runners.messenger.usageMissing') }}</dd>
          </dl>
          <p v-else class="mnote mono">{{ t('runners.messenger.disabled') }}</p>
        </section>

        <section class="msec">
          <h3 class="msec-title mono">
            {{ t('runners.messenger.visibleSessions') }}
            <span class="dim">{{ t('runners.messenger.visibleSessionsHint') }}</span>
          </h3>
          <div class="mactions">
            <button
              class="act act--primary mono"
              type="button"
              data-test="list-agents"
              :disabled="!!listBlock || listLoading"
              :title="listBlock || t('runners.messenger.listTitle')"
              @click="loadAgents(false)"
            >{{ listLoading ? t('runners.messenger.listing') : t('runners.messenger.list') }}</button>
            <button
              v-if="listing"
              class="act mono"
              type="button"
              :disabled="listLoading"
              :title="t('runners.messenger.refreshTitle')"
              @click="loadAgents(true)"
            >{{ t('runners.messenger.refresh') }}</button>
            <span v-if="listBlock" class="mnote mono" data-test="list-block">{{ listBlock }}</span>
          </div>
          <p v-if="listError" class="merr mono" data-test="list-error">{{ listError }}</p>
          <template v-if="listing">
            <p class="mnote mono">
              {{ t('runners.messenger.listSummary', { n: listing.agents.length, mode: listing.cached ? t('runners.messenger.cached') : t('runners.messenger.fresh'), ago: fmtAgo(Math.floor(listing.fetched_at / 1000), nowSec) }) }}
              <span v-if="listing.self"> · {{ t('runners.messenger.self', { name: listing.self }) }}</span>
            </p>
            <div v-if="rows.length" class="mtable mono" data-test="agents-table">
              <div class="mtr mthead">
                <span>{{ t('runners.messenger.col.name') }}</span><span>{{ t('runners.messenger.col.status') }}</span><span>{{ t('runners.messenger.col.cwd') }}</span><span>{{ t('runners.messenger.col.started') }}</span><span>gofer</span><span></span>
              </div>
              <div v-for="row in rows" :key="row.agent.short_id || row.agent.name" class="mtr">
                <span class="mname" :title="`${row.agent.name} [${row.agent.short_id ?? ''}]`">{{ row.agent.name }}<small v-if="row.agent.short_id" class="dim"> [{{ row.agent.short_id }}]</small></span>
                <span>{{ row.agent.status || '—' }}</span>
                <span class="mpath" :title="row.session?.cwd || row.agent.cwd || t('runners.messenger.cwdTitle')">{{ row.session?.cwd || row.agent.cwd || '—' }}</span>
                <span>{{ row.agent.started || row.agent.last_activity || '—' }}</span>
                <span :class="row.session ? 'tone-ok' : 'dim'" :data-test="row.session ? 'reg-yes' : 'reg-no'">{{ row.session ? t('runners.messenger.registered') : t('runners.messenger.unregistered') }}</span>
                <span>
                  <button
                    class="act mono"
                    type="button"
                    data-test="talk-to"
                    :disabled="!!messageBlockReason(row)"
                    :title="messageBlockReason(row) || t('runners.messenger.talkTitle')"
                    @click="row.session && talkTo(row.session.session_id)"
                  >{{ t('runners.messenger.talk') }}</button>
                </span>
              </div>
            </div>
            <p v-else class="mnote mono">{{ t('runners.messenger.noOtherSessions') }}</p>
            <button class="link-btn mono" type="button" @click="rawOpen = !rawOpen">{{ rawOpen ? t('runners.messenger.hideRaw') : t('runners.messenger.raw') }}</button>
            <pre v-if="rawOpen" class="mpre mono">{{ listing.raw_output }}</pre>
          </template>
        </section>

        <section class="msec">
          <h3 class="msec-title mono">{{ t('runners.messenger.rotations') }} <span class="dim">{{ t('runners.messenger.rotationsHint') }}</span></h3>
          <ul v-if="rotations.length" class="mdeliv mono" data-test="messenger-rotations">
            <li v-for="(r, i) in rotations" :key="`${r.at}-${i}`">
              <span class="dtime" :title="fmtDateTime(r.at)">{{ fmtAgo(r.at, nowSec) }}</span>
              <span class="dop">{{ rotationReasonLabel(r.reason) }}</span>
              <span class="dres">
                {{ t('runners.messenger.handledCount', { n: r.messages ?? 0 }) }}<template v-if="formatAge(r.age_sec)"> · {{ t('runners.messenger.aliveFor', { age: formatAge(r.age_sec) }) }}</template>
              </span>
              <span v-if="usageText(r.usage)" class="dmsg">{{ usageText(r.usage) }}</span>
            </li>
          </ul>
          <p v-else class="mnote mono">{{ t('runners.messenger.noRotations') }}</p>
        </section>

        <section class="msec">
          <h3 class="msec-title mono">{{ t('runners.messenger.deliveries') }} <span class="dim">{{ t('runners.messenger.deliveriesHint') }}</span></h3>
          <ul v-if="deliveries.length" class="mdeliv mono" data-test="deliveries">
            <li v-for="(d, i) in deliveries" :key="`${d.at}-${i}`" :class="d.ok ? 'dok' : 'dfail'">
              <span class="dtime" :title="fmtDateTime(d.at)">{{ fmtAgo(d.at, nowSec) }}</span>
              <span class="dop">{{ opLabel(d.op) }}</span>
              <span v-if="d.target" class="dtarget">→ {{ d.target }}</span>
              <span class="dres">{{ d.ok ? t('runners.messenger.ok') : t('runners.messenger.fail') }}<template v-if="d.duration_ms"> · {{ (d.duration_ms / 1000).toFixed(1) }}s</template></span>
              <span v-if="d.error" class="derr">{{ d.error }}</span>
              <span v-else-if="d.message" class="dmsg">{{ d.message }}</span>
            </li>
          </ul>
          <p v-else class="mnote mono">{{ t('runners.messenger.noDeliveries') }}</p>
        </section>

        <section class="msec">
          <h3 class="msec-title mono">{{ t('runners.messenger.stderr') }} <span class="dim">{{ t('runners.messenger.stderrHint') }}</span></h3>
          <pre v-if="snap?.stderr_tail" class="mpre mono" data-test="stderr">{{ snap.stderr_tail }}</pre>
          <p v-else class="mnote mono">{{ t('runners.messenger.noStderr') }}</p>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mdrawer-overlay {
  position: fixed;
  inset: 0;
  z-index: 80;
  display: flex;
  justify-content: flex-end;
  background: rgba(0, 0, 0, 0.6);
}
.mdrawer-panel {
  display: flex;
  flex-direction: column;
  width: min(640px, 94vw);
  height: 100%;
  background: var(--surface);
  border-left: 1px solid var(--line);
  overflow: hidden;
}
.mdrawer-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--line);
}
.mdrawer-title {
  color: var(--ink);
  font-size: 13px;
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.mdrawer-body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 14px 24px;
}
.msec { margin-bottom: 20px; }
.msec-title {
  margin: 0 0 8px;
  font-size: 12px;
  letter-spacing: 0.04em;
  color: var(--ink);
}
.dim { color: var(--ink-3); font-weight: 400; }
.mnote { margin: 6px 0; font-size: 11px; color: var(--ink-3); word-break: break-word; }
.merr { margin: 6px 0; font-size: 12px; color: var(--fail); border: 1px solid var(--fail); border-radius: var(--r-chip); padding: 6px 8px; word-break: break-word; }
.mdl { display: grid; grid-template-columns: 84px 1fr; gap: 4px 10px; margin: 0; font-size: 12px; color: var(--ink); }
.mdl dt { color: var(--ink-3); }
.mdl dd { margin: 0; word-break: break-word; }
.mstatus {
  font-size: 11px;
  border: 1px solid currentColor;
  border-radius: var(--r-chip);
  padding: 0 7px;
  line-height: 1.6;
}
.mtone--idle { color: var(--done); }
.mtone--busy { color: var(--run); }
.mtone--stopped { color: var(--ink-3); }
.mtone--unknown { color: var(--ink-3); border-style: dashed; }
.mactions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.act {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: transparent;
  color: var(--ink-3);
  padding: 4px 8px;
  font-size: 11px;
  cursor: pointer;
}
.act:hover:not(:disabled) { color: var(--ink); border-color: var(--ink); }
.act:disabled { cursor: not-allowed; opacity: 0.5; }
.act--primary { color: var(--accent); border-color: var(--accent); }
.link-btn { background: none; border: 0; color: var(--accent); font-size: 11px; cursor: pointer; padding: 4px 0; }
.mtable { margin-top: 6px; font-size: 11px; color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-chip); overflow-x: auto; }
.mtr {
  display: grid;
  grid-template-columns: minmax(120px, 1.6fr) 56px minmax(100px, 1.4fr) 70px 58px 86px;
  gap: 8px;
  align-items: center;
  padding: 6px 8px;
  border-top: 1px solid var(--line);
  min-width: 560px;
}
.mthead { color: var(--ink-3); border-top: 0; }
.mname, .mpath { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tone-ok { color: var(--done); }
.mpre {
  margin: 6px 0 0;
  padding: 8px;
  max-height: 220px;
  overflow: auto;
  font-size: 11px;
  color: var(--ink);
  background: var(--sunken);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  white-space: pre-wrap;
  word-break: break-all;
}
.mdeliv { list-style: none; margin: 0; padding: 0; font-size: 11px; }
.mdeliv li { display: flex; flex-wrap: wrap; gap: 4px 10px; padding: 6px 0; border-top: 1px solid var(--line); color: var(--ink); }
.mdeliv li:first-child { border-top: 0; }
.dtime { color: var(--ink-3); }
.dop { color: var(--accent); }
.dok .dres { color: var(--done); }
.dfail .dres, .derr { color: var(--fail); }
.dmsg { color: var(--ink-3); flex-basis: 100%; word-break: break-word; }
.derr { flex-basis: 100%; word-break: break-word; }
@media (max-width: 760px) {
  .mdrawer-panel { width: 100vw; }
  .mdl { grid-template-columns: 72px 1fr; }
  /* 手机：会话表改成每行一张小卡片，“给它传话”不再被挤到横向滚动之外 */
  .mtable { overflow-x: visible; }
  .mthead { display: none; }
  .mtr { grid-template-columns: 1fr 1fr; min-width: 0; row-gap: 4px; }
  .mtr > :nth-child(1), .mtr > :nth-child(3), .mtr > :nth-child(6) { grid-column: 1 / -1; }
  .mpath { white-space: normal; word-break: break-all; }
}
</style>
