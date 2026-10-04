<script setup lang="ts">
// 传话人抽屉：一个 runner 上常驻的 Claude SendMessage 进程的状态页。
//  - 状态 / 启动时间 / 最近使用 / 空闲退出倒计时（local 读 server 进程内快照，worker 读心跳）
//  - 最近 20 次投递摘要（传话 / 列出会话，成功或失败原因；消息只显示截断摘要）
//  - 子进程 stderr 尾部（4KB），排查“为什么传话失败”
//  - “列出可见会话”：问传话人 ListAgents，并与 gofer 登记的会话对照，行内一键去传话
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listAgentSessions, listMessengerAgents } from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import type { AgentSession, MessengerAgentsResp, Runner } from '../api/types'
import {
  crossReference,
  idleLeftText,
  messageBlockReason,
  messengerDisplay,
  messengerListBlock,
  opLabel,
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
    <div class="mdrawer-panel" role="dialog" aria-label="传话人" data-test="messenger-drawer">
      <header class="mdrawer-head">
        <div class="mdrawer-title mono">
          传话人 · {{ runner.name }}
          <span class="mstatus mono" :class="`mtone--${display.tone}`" data-test="drawer-status">{{ display.text }}</span>
        </div>
        <button class="act mono" type="button" @click="emit('close')">关闭</button>
      </header>

      <div class="mdrawer-body">
        <section class="msec">
          <h3 class="msec-title mono">状态</h3>
          <p v-if="!display.known && isWorker && runner.status === 'connected'" class="mnote mono">
            这台 worker 还没有上报传话人状态（旧版本，或刚连上还没有心跳）。升级 worker 后这里会显示实时状态。
          </p>
          <dl v-else-if="snap" class="mdl mono">
            <dt>状态</dt><dd>{{ display.text }}<span v-if="idleLeftText(snap, nowSec)" class="dim"> · {{ idleLeftText(snap, nowSec) }}</span></dd>
            <dt>进程启动</dt><dd>{{ snap.started_at ? `${fmtDateTime(snap.started_at)}（${fmtAgo(snap.started_at, nowSec)}）` : '未启动' }}</dd>
            <dt>最近使用</dt><dd>{{ snap.last_used_at ? `${fmtDateTime(snap.last_used_at)}（${fmtAgo(snap.last_used_at, nowSec)}）` : '还没用过' }}</dd>
          </dl>
          <p v-else class="mnote mono">server 没有启用会话传话。</p>
        </section>

        <section class="msec">
          <h3 class="msec-title mono">
            可见会话
            <span class="dim">（传话人能 SendMessage 的会话）</span>
          </h3>
          <div class="mactions">
            <button
              class="act act--primary mono"
              type="button"
              data-test="list-agents"
              :disabled="!!listBlock || listLoading"
              :title="listBlock || '让传话人跑一次 ListAgents（会调用一次模型，约几秒）'"
              @click="loadAgents(false)"
            >{{ listLoading ? '查询中…' : '列出可见会话' }}</button>
            <button
              v-if="listing"
              class="act mono"
              type="button"
              :disabled="listLoading"
              title="忽略 30 秒缓存，重新问一次"
              @click="loadAgents(true)"
            >刷新</button>
            <span v-if="listBlock" class="mnote mono" data-test="list-block">{{ listBlock }}</span>
          </div>
          <p v-if="listError" class="merr mono" data-test="list-error">{{ listError }}</p>
          <template v-if="listing">
            <p class="mnote mono">
              {{ listing.agents.length }} 个会话 · {{ listing.cached ? '缓存结果' : '刚刚查询' }}（{{ fmtAgo(Math.floor(listing.fetched_at / 1000), nowSec) }}）
              <span v-if="listing.self"> · 传话人自己：{{ listing.self }}</span>
            </p>
            <div v-if="rows.length" class="mtable mono" data-test="agents-table">
              <div class="mtr mthead">
                <span>名称</span><span>状态</span><span>目录</span><span>启动</span><span>gofer</span><span></span>
              </div>
              <div v-for="row in rows" :key="row.agent.short_id || row.agent.name" class="mtr">
                <span class="mname" :title="`${row.agent.name} [${row.agent.short_id ?? ''}]`">{{ row.agent.name }}<small v-if="row.agent.short_id" class="dim"> [{{ row.agent.short_id }}]</small></span>
                <span>{{ row.agent.status || '—' }}</span>
                <span class="mpath" :title="row.session?.cwd || row.agent.cwd || 'ListAgents 不提供目录；已登记的会话取 gofer 记录的目录'">{{ row.session?.cwd || row.agent.cwd || '—' }}</span>
                <span>{{ row.agent.started || row.agent.last_activity || '—' }}</span>
                <span :class="row.session ? 'tone-ok' : 'dim'" :data-test="row.session ? 'reg-yes' : 'reg-no'">{{ row.session ? '已登记' : '未登记' }}</span>
                <span>
                  <button
                    class="act mono"
                    type="button"
                    data-test="talk-to"
                    :disabled="!!messageBlockReason(row)"
                    :title="messageBlockReason(row) || '打开这个会话，给它传话'"
                    @click="row.session && talkTo(row.session.session_id)"
                  >给它传话</button>
                </span>
              </div>
            </div>
            <p v-else class="mnote mono">传话人当前看不到别的会话。</p>
            <button class="link-btn mono" type="button" @click="rawOpen = !rawOpen">{{ rawOpen ? '收起原始输出' : '原始输出' }}</button>
            <pre v-if="rawOpen" class="mpre mono">{{ listing.raw_output }}</pre>
          </template>
        </section>

        <section class="msec">
          <h3 class="msec-title mono">最近投递 <span class="dim">（最多 20 条，新的在上；消息只留 200 字摘要）</span></h3>
          <ul v-if="deliveries.length" class="mdeliv mono" data-test="deliveries">
            <li v-for="(d, i) in deliveries" :key="`${d.at}-${i}`" :class="d.ok ? 'dok' : 'dfail'">
              <span class="dtime" :title="fmtDateTime(d.at)">{{ fmtAgo(d.at, nowSec) }}</span>
              <span class="dop">{{ opLabel(d.op) }}</span>
              <span v-if="d.target" class="dtarget">→ {{ d.target }}</span>
              <span class="dres">{{ d.ok ? '成功' : '失败' }}<template v-if="d.duration_ms"> · {{ (d.duration_ms / 1000).toFixed(1) }}s</template></span>
              <span v-if="d.error" class="derr">{{ d.error }}</span>
              <span v-else-if="d.message" class="dmsg">{{ d.message }}</span>
            </li>
          </ul>
          <p v-else class="mnote mono">还没有投递记录。</p>
        </section>

        <section class="msec">
          <h3 class="msec-title mono">stderr 尾部 <span class="dim">（4KB）</span></h3>
          <pre v-if="snap?.stderr_tail" class="mpre mono" data-test="stderr">{{ snap.stderr_tail }}</pre>
          <p v-else class="mnote mono">没有 stderr 输出。</p>
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
  background: var(--panel);
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
  color: var(--paper);
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
  color: var(--paper);
}
.dim { color: var(--queue); font-weight: 400; }
.mnote { margin: 6px 0; font-size: 11px; color: var(--queue); word-break: break-word; }
.merr { margin: 6px 0; font-size: 12px; color: var(--fail); border: 1px solid var(--fail); border-radius: var(--radius); padding: 6px 8px; word-break: break-word; }
.mdl { display: grid; grid-template-columns: 84px 1fr; gap: 4px 10px; margin: 0; font-size: 12px; color: var(--paper); }
.mdl dt { color: var(--queue); }
.mdl dd { margin: 0; word-break: break-word; }
.mstatus {
  font-size: 11px;
  border: 1px solid currentColor;
  border-radius: var(--radius);
  padding: 0 7px;
  line-height: 1.6;
}
.mtone--idle { color: var(--done); }
.mtone--busy { color: var(--run); }
.mtone--stopped { color: var(--queue); }
.mtone--unknown { color: var(--queue); border-style: dashed; }
.mactions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.act {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: transparent;
  color: var(--queue);
  padding: 4px 8px;
  font-size: 11px;
  cursor: pointer;
}
.act:hover:not(:disabled) { color: var(--paper); border-color: var(--paper); }
.act:disabled { cursor: not-allowed; opacity: 0.5; }
.act--primary { color: var(--phosphor); border-color: var(--phosphor); }
.link-btn { background: none; border: 0; color: var(--phosphor); font-size: 11px; cursor: pointer; padding: 4px 0; }
.mtable { margin-top: 6px; font-size: 11px; color: var(--paper); border: 1px solid var(--line); border-radius: var(--radius); overflow-x: auto; }
.mtr {
  display: grid;
  grid-template-columns: minmax(120px, 1.6fr) 56px minmax(100px, 1.4fr) 70px 58px 86px;
  gap: 8px;
  align-items: center;
  padding: 6px 8px;
  border-top: 1px solid var(--line);
  min-width: 560px;
}
.mthead { color: var(--queue); border-top: 0; }
.mname, .mpath { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tone-ok { color: var(--done); }
.mpre {
  margin: 6px 0 0;
  padding: 8px;
  max-height: 220px;
  overflow: auto;
  font-size: 11px;
  color: var(--paper);
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  white-space: pre-wrap;
  word-break: break-all;
}
.mdeliv { list-style: none; margin: 0; padding: 0; font-size: 11px; }
.mdeliv li { display: flex; flex-wrap: wrap; gap: 4px 10px; padding: 6px 0; border-top: 1px solid var(--line); color: var(--paper); }
.mdeliv li:first-child { border-top: 0; }
.dtime { color: var(--queue); }
.dop { color: var(--phosphor); }
.dok .dres { color: var(--done); }
.dfail .dres, .derr { color: var(--fail); }
.dmsg { color: var(--queue); flex-basis: 100%; word-break: break-word; }
.derr { flex-basis: 100%; word-break: break-word; }
@media (max-width: 640px) {
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
