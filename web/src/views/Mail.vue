<script setup lang="ts">
// 信箱页（design 2026-10-20「信箱按对话显示、会话以人物呈现」，计划 M2）：一个信箱一个页面——
//  - 左：对话列表（服务端按参与方对聚合的 conversations 接口，一行一个对话）——两个人物卡、
//    最后一封的纯文本摘要、未读数、急件、信件数、活动时间；页签「全部 / 收件 / 发件 / 待指派」；
//  - 右：所选对话的时间线（GET /v1/mail?peer=&with=，时间正序）——气泡按「我方在右」分边，
//    回信标「回复 #N」可跳到被回复的那封，正文按 markdown 渲染（MarkdownBlock，含消毒）、
//    长信折叠；底部回复框；
//  - 待指派：原因、候选（服务端在发信 / 改投的回答里带回）、改投（改地址重发）、撤回；
//  - 顶部「写信」：地址与急件开关。
// 规则（地址解析校验、对话 key、气泡方向、人物卡取色、摘要去 markdown、查询参数）都在
// utils/mail.ts 与 utils/mailConversations.ts，这里只管渲染与请求。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ackMail,
  cancelMail,
  listMail,
  listMailConversations,
  readdressMail,
  replyMail,
  sendMail,
} from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import type { MailCandidate, MailConversation, MailMessage } from '../api/types'
import { useWhoami } from '../store/whoami'
import { pageWriteBlock } from '../utils/pageGate'
import ExperimentalBadge from '../components/ExperimentalBadge.vue'
import MarkdownBlock from '../components/MarkdownBlock.vue'
import MailPersonaChip from '../components/mail/PersonaChip.vue'
import SessionDrawer from '../components/SessionDrawer.vue'
import {
  buildMailReaddress,
  buildMailReply,
  buildMailSend,
  MAIL_FILTERS,
  mailFilterFromQuery,
  mailListQuery,
  mailOutgoing,
  mailPartyLabel,
  mailStateLabel,
  mailStateTone,
  type MailFilter,
  type MailIdentity,
} from '../utils/mail'
import {
  bubbleSide,
  groupMailConversations,
  mailConversationBox,
  mailConversationKey,
  mailConversationSeq,
  mailFoldedBody,
  mailLongBody,
  mailPartyAddress,
  mailReplyLabel,
  mailSummary,
  mailTimelineAddressable,
  mailTimelineQuery,
  mailUnread,
  personaLabel,
  replyTarget,
  sortMailTimeline,
} from '../utils/mailConversations'

const route = useRoute()
const router = useRouter()
const { whoami } = useWhoami()

// 左栏：对话（服务端聚合，或「待指派」/ 会话深链时本地按参与方对分组）。
const rows = ref<MailConversation[]>([])
// 右栏：所选对话的时间线（时间正序）。
const timeline = ref<MailMessage[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))
const activeKey = ref('')
const openSid = ref('')
// 长信折叠：默认折叠，记下展开过的那几封。
const expandedIds = ref<Set<number>>(new Set())
// 「回复 #N」跳过去时闪一下被回复的那封。
const highlightId = ref(0)
// 窄屏：列表与时间线切换显示。
const mobileDetail = ref(false)

const filter = ref<MailFilter>(mailFilterFromQuery(route.query))
const sessionFilter = ref(queryValue(route.query.session))

// 服务端只在发信 / 改投的回答里带候选，收件箱列表不带：这里按信件 id 记下见过的那几组，
// 打开待指派信件时能一键填进改投输入框。
const candidates = ref<Record<number, MailCandidate[]>>({})

const composeOpen = ref(false)
const compose = ref({ to: '', body: '', urgent: false })
const composeError = ref('')
const sending = ref(false)

const reply = ref({ body: '', urgent: false })
const replyError = ref('')
const replying = ref(false)

const readdressDraft = ref<Record<number, string>>({})
const readdressError = ref('')
const acting = ref(false)

function queryValue(v: unknown): string {
  const x = Array.isArray(v) ? v[0] : v
  return typeof x === 'string' ? x.trim() : ''
}

// 我在这台 web 上的身份：调用方 id；带 ?session= 进来时那个会话也算「我」（会话抽屉里
// 点进来，要把它发的信认成我方，气泡在自己这一侧）。
const me = computed<MailIdentity>(() => ({ caller: whoami.value?.caller_id ?? '', session: sessionFilter.value }))

// 回信（POST /v1/mail/{id}/reply）：信的两端任意一端是自己就不需要权限（收发都算自己的信）；
// 投到别人的会话要 session.reply.others、别人的 job / 调用方要 job.manage.others——
// 路由上就是 InHandler(permDeny, 这两条)，不是同时要求。只读身份一律灰显（gofer-bp9c，
// 判定见 utils/pageGate.ts，服务端仍是权威）。
const replyWriteBlock = computed(() => {
  const target = replyTarget(timeline.value, me.value)
  const caller = whoami.value?.caller_id
  const own = !!target && !!caller && (target.from.caller === caller || target.to.caller === caller)
  return pageWriteBlock(whoami.value, {
    ownerId: own ? caller : undefined,
    anyPerms: ['session.reply.others', 'job.manage.others'],
  })
})

const active = computed(() => rows.value.find((r) => r.key === activeKey.value) ?? null)
const seq = computed(() => mailConversationSeq(timeline.value))
// 「待指派」与会话深链没有对应的 conversations 过滤：按信件在本地分组成对话。
const groupedLocally = computed(() => filter.value === 'held' || !!sessionFilter.value)
const activeTitle = computed(() => {
  const conv = active.value
  if (!conv) return ''
  return `${personaLabel(conv.a)} / ${personaLabel(conv.b)}`
})

async function loadRows(): Promise<void> {
  if (groupedLocally.value) {
    const resp = await listMail(mailListQuery(filter.value, { session: sessionFilter.value }))
    rows.value = groupMailConversations(resp.mail ?? [])
    return
  }
  const resp = await listMailConversations({ box: mailConversationBox(filter.value), limit: 100 })
  rows.value = resp.conversations ?? []
}

async function loadTimeline(): Promise<void> {
  const conv = active.value
  if (!conv) {
    timeline.value = []
    return
  }
  const q = mailTimelineQuery(conv, filter.value)
  const addressable = mailTimelineAddressable(q)
  // 一端的地址没解析到人（topic: / path: 之类的待指派信）时服务端不许用 peer= / with=：
  // 退回按对话 key 本地过滤。
  const resp = await listMail({
    box: q.box,
    state: q.state,
    peer: addressable ? q.peer : undefined,
    with: addressable ? q.with : undefined,
    session: sessionFilter.value || undefined,
    limit: 200,
  })
  const all = resp.mail ?? []
  timeline.value = sortMailTimeline(addressable ? all : all.filter((m) => mailConversationKey(m) === q.key))
}

async function load(silent = false): Promise<void> {
  if (!silent) loading.value = true
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    await loadRows()
    if (!rows.value.some((r) => r.key === activeKey.value)) {
      activeKey.value = rows.value[0]?.key ?? ''
    }
    await loadTimeline()
    error.value = ''
  } catch (e) {
    rows.value = []
    timeline.value = []
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

// 页签切换写回地址栏（可分享 / 可收藏），再由 watch 重新拉；Today 卡与抽屉的深链也是
// 同一套参数（?state=held / ?session=<sid>）。
function setFilter(f: MailFilter): void {
  const query: Record<string, string> = {}
  if (f !== 'all') query.filter = f
  if (sessionFilter.value) query.session = sessionFilter.value
  void router.replace({ path: '/mail', query })
}

function clearSession(): void {
  const query: Record<string, string> = {}
  if (filter.value !== 'all') query.filter = filter.value
  void router.replace({ path: '/mail', query })
}

watch(
  () => [route.query.filter, route.query.box, route.query.state, route.query.session],
  () => {
    filter.value = mailFilterFromQuery(route.query)
    sessionFilter.value = queryValue(route.query.session)
    activeKey.value = ''
    void load()
  },
)

// 打开一个对话 = 读过它的信：把自己收的未读信件逐封 ack（读 = 送达；服务端只对收件方生效）。
async function selectConversation(key: string): Promise<void> {
  activeKey.value = key
  mobileDetail.value = true
  replyError.value = ''
  notice.value = ''
  highlightId.value = 0
  try {
    await loadTimeline()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    timeline.value = []
    return
  }
  await ackUnread()
}

async function ackUnread(): Promise<void> {
  const unread = timeline.value.filter((m) => mailUnread(m) && !mailOutgoing(m, me.value))
  if (!unread.length) return
  try {
    await Promise.all(unread.map((m) => ackMail(m.id)))
    await load(true)
  } catch {
    // 读信失败不影响看信：状态保持原样，服务端仍是权威。
  }
}

function backToList(): void {
  mobileDetail.value = false
}

async function sendCompose(): Promise<void> {
  const built = buildMailSend(compose.value)
  composeError.value = built.error
  if (!built.req) return
  sending.value = true
  try {
    const resp = await sendMail(built.req)
    noteSendResult(resp.mail, resp.held, resp.candidates ?? [])
    compose.value = { to: '', body: '', urgent: false }
    composeOpen.value = false
    await load(true)
  } catch (e) {
    composeError.value = e instanceof Error ? e.message : String(e)
  } finally {
    sending.value = false
  }
}

function noteSendResult(m: MailMessage, held: boolean, cands: MailCandidate[]): void {
  if (cands.length) candidates.value = { ...candidates.value, [m.id]: cands }
  if (held) {
    notice.value = `地址没有唯一命中，信件 #${m.id} 已转「待指派」${cands.length ? `，候选 ${cands.length} 个` : ''}。`
  } else {
    notice.value = `已发出 #${m.id}（${mailStateLabel(m.state)}）。`
  }
}

async function sendReply(): Promise<void> {
  // 回复送回原发件方地址（不是我发出的那封的最新一封）。
  const target = replyTarget(timeline.value, me.value)
  if (!target) return
  const built = buildMailReply(reply.value.body, reply.value.urgent)
  replyError.value = built.error
  if (!built.req) return
  replying.value = true
  try {
    const resp = await replyMail(target.id, built.req)
    noteSendResult(resp.mail, resp.held, resp.candidates ?? [])
    reply.value = { body: '', urgent: false }
    await load(true)
  } catch (e) {
    replyError.value = e instanceof Error ? e.message : String(e)
  } finally {
    replying.value = false
  }
}

async function doReaddress(m: MailMessage): Promise<void> {
  const built = buildMailReaddress(readdressDraft.value[m.id] ?? '')
  readdressError.value = built.error
  if (!built.req) return
  acting.value = true
  try {
    const resp = await readdressMail(m.id, built.req)
    noteSendResult(resp.mail, resp.held, resp.candidates ?? [])
    await load(true)
  } catch (e) {
    readdressError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = false
  }
}

async function doCancel(m: MailMessage): Promise<void> {
  acting.value = true
  try {
    await cancelMail(m.id)
    notice.value = `已撤回 #${m.id}。`
    await load(true)
  } catch (e) {
    readdressError.value = e instanceof Error ? e.message : String(e)
  } finally {
    acting.value = false
  }
}

function fillReaddress(m: MailMessage, to: string): void {
  readdressDraft.value = { ...readdressDraft.value, [m.id]: to }
}

// 回信角标与跳转（设计「同一个 thread 里的回信用细线串起来，标注回复 #N，点击跳到被回复的那封」）。
function replyLabel(m: MailMessage): string {
  return mailReplyLabel(m, seq.value)
}

function jumpToReply(m: MailMessage): void {
  if (!m.reply_to) return
  const target = document.getElementById(`mail-${m.reply_to}`)
  if (!target) return
  target.scrollIntoView({ behavior: 'smooth', block: 'center' })
  highlightId.value = m.reply_to
  window.setTimeout(() => {
    if (highlightId.value === m.reply_to) highlightId.value = 0
  }, 1600)
}

function bodyText(m: MailMessage): string {
  return expandedIds.value.has(m.id) ? m.body : mailFoldedBody(m.body)
}

function toggleFold(id: number): void {
  const next = new Set(expandedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedIds.value = next
}

// 人物卡上的会话可点开（与会话页 / 抽屉里的打开方式一致：挂一个 SessionDrawer）。
function openSession(sid: string): void {
  openSid.value = sid
}

onMounted(() => {
  void load()
})
</script>

<template>
  <section class="mail-page">
    <header class="mail-head">
      <h1 class="mail-title">信箱<ExperimentalBadge /></h1>
      <nav class="mail-tabs" aria-label="信箱过滤">
        <button
          v-for="f in MAIL_FILTERS"
          :key="f.key"
          class="tab"
          :class="{ 'tab--active': filter === f.key }"
          type="button"
          :data-test="`mail-tab-${f.key}`"
          @click="setFilter(f.key)"
        >
          {{ f.label }}
        </button>
      </nav>
      <span v-if="sessionFilter" class="filter-chip mono" data-test="mail-session-filter">
        会话 {{ sessionFilter }}
        <button class="chip-x" type="button" title="去掉会话过滤" @click="clearSession">×</button>
      </span>
      <button class="act mono" type="button" data-test="mail-refresh" @click="load()">刷新</button>
      <button class="act act--primary mono" type="button" data-test="mail-compose-toggle" @click="composeOpen = !composeOpen">
        {{ composeOpen ? '收起' : '写信' }}
      </button>
    </header>

    <form v-if="composeOpen" class="compose" data-test="mail-compose" @submit.prevent="sendCompose">
      <input
        v-model="compose.to"
        class="compose-to mono"
        type="text"
        placeholder="session:<sid> / job:<id> / topic:<关键词> / path:<子路径> / caller"
        data-test="mail-to"
      />
      <textarea
        v-model="compose.body"
        class="compose-body"
        rows="3"
        placeholder="正文（markdown，单封上限 32 KB）"
        data-test="mail-body"
      ></textarea>
      <div class="compose-row">
        <label class="urgent-pick"
          ><input v-model="compose.urgent" type="checkbox" data-test="mail-urgent" /> 急件（在收件会话的下一轮内注入）</label
        >
        <button class="act act--primary mono" type="submit" :disabled="sending" data-test="mail-send">
          {{ sending ? '发送中…' : '发送' }}
        </button>
      </div>
      <p v-if="composeError" class="err" data-test="mail-compose-error">{{ composeError }}</p>
    </form>

    <p v-if="notice" class="notice" data-test="mail-notice">{{ notice }}</p>
    <p v-if="error" class="err" data-test="mail-error">{{ error }}</p>

    <div class="split" :class="{ 'split--detail': mobileDetail }">
      <aside class="convs" aria-label="对话">
        <p v-if="loading" class="dim pad">加载中…</p>
        <p v-else-if="!rows.length" class="dim pad" data-test="mail-empty">没有信件</p>
        <button
          v-for="c in rows"
          :key="c.key"
          class="conv"
          :class="{ 'conv--active': c.key === activeKey }"
          type="button"
          data-test="mail-conversation"
          @click="selectConversation(c.key)"
        >
          <span class="conv-people">
            <MailPersonaChip variant="row" :party="c.a" :now-sec="nowSec" @open-session="openSession" />
            <span class="conv-arrow dim">↔</span>
            <MailPersonaChip variant="row" :party="c.b" :now-sec="nowSec" @open-session="openSession" />
            <span v-if="c.urgent" class="tag tag--urgent" data-test="mail-conv-urgent">急件</span>
            <span v-if="c.unread" class="unread" data-test="mail-conv-unread">{{ c.unread }}</span>
          </span>
          <span class="conv-excerpt dim" data-test="mail-conv-excerpt">{{ mailSummary(c.last_mail.body, 80) }}</span>
          <span class="conv-meta dim mono">{{ c.count }} 封 · {{ fmtAgo(c.activity_at, nowSec) }}</span>
        </button>
      </aside>

      <section class="detail" aria-label="对话时间线">
        <template v-if="active">
          <header class="detail-head">
            <button class="act mono back" type="button" data-test="mail-back" @click="backToList">← 列表</button>
            <MailPersonaChip :party="active.a" :now-sec="nowSec" @open-session="openSession" />
            <span class="dim">↔</span>
            <MailPersonaChip :party="active.b" :now-sec="nowSec" @open-session="openSession" />
            <span class="dim mono">{{ timeline.length }} 封</span>
          </header>

          <div class="timeline">
            <article
              v-for="m in timeline"
              :id="`mail-${m.id}`"
              :key="m.id"
              class="bubble"
              :class="[
                `bubble--${bubbleSide(m, active, me)}`,
                { 'bubble--reply': !!m.reply_to, 'bubble--hot': highlightId === m.id, 'bubble--urgent': !!m.urgent },
              ]"
              :data-side="bubbleSide(m, active, me)"
              data-test="mail-letter"
            >
              <header class="bubble-head">
                <MailPersonaChip variant="row" :party="m.from" :now-sec="nowSec" @open-session="openSession" />
                <span class="dim mono bubble-to" :title="mailPartyAddress(m.to) || m.to_address">→ {{ m.to_address || mailPartyLabel(m.to) }}</span>
                <button v-if="replyLabel(m)" class="reply-ref mono" type="button" data-test="mail-reply-ref" @click="jumpToReply(m)">
                  {{ replyLabel(m) }}
                </button>
                <span v-if="m.urgent" class="tag tag--urgent">急件</span>
                <span class="letter-state mono" :class="`tone--${mailStateTone(m.state)}`" data-test="mail-letter-state">
                  {{ mailStateLabel(m.state) }}
                </span>
                <span class="dim mono" :title="fmtDateTime(m.created_at)">#{{ m.id }} · {{ fmtAgo(m.created_at, nowSec) }}</span>
              </header>

              <!-- 正文是别的 agent 会话写的 markdown：走 MarkdownBlock（marked + DOMPurify 消毒）。 -->
              <div class="bubble-body" data-test="mail-letter-body">
                <MarkdownBlock :text="bodyText(m)" />
              </div>
              <button v-if="mailLongBody(m.body)" class="fold mono" type="button" data-test="mail-letter-fold" @click="toggleFold(m.id)">
                {{ expandedIds.has(m.id) ? '收起' : `展开全文（${m.body.length} 字）` }}
              </button>

              <div v-if="m.state === 'held'" class="held" data-test="mail-held">
                <p class="held-why">
                  <b>待指派</b>
                  <span v-if="m.last_error" class="dim">· {{ m.last_error }}</span>
                  <!-- 候选与「新建 job / 丢弃」在收集箱条目里：待指派时生成的条目带推荐的分派目标。 -->
                  <RouterLink
                    v-if="m.capture_item_id"
                    class="held-item"
                    data-test="mail-held-item"
                    :to="{ path: '/work', query: { id: m.capture_item_id } }"
                  >在收集箱处理</RouterLink>
                </p>
                <div v-if="candidates[m.id]?.length" class="cands" data-test="mail-candidates">
                  <span class="dim">候选：</span>
                  <button
                    v-for="c in candidates[m.id]"
                    :key="`${c.kind}:${c.id}`"
                    class="cand mono"
                    type="button"
                    :title="(c.why ?? []).join(' / ')"
                    @click="fillReaddress(m, `${c.kind}:${c.id}`)"
                  >
                    {{ c.display || `${c.kind}:${c.id}` }}
                  </button>
                </div>
                <div class="readdress">
                  <input
                    v-model="readdressDraft[m.id]"
                    class="mono"
                    type="text"
                    placeholder="改投到：session:… / job:… / topic:… / path:…"
                    data-test="mail-readdress-to"
                  />
                  <button class="act act--primary mono" type="button" :disabled="acting" data-test="mail-readdress" @click="doReaddress(m)">
                    改投
                  </button>
                  <button class="act mono" type="button" :disabled="acting" data-test="mail-cancel" @click="doCancel(m)">撤回</button>
                </div>
              </div>
            </article>
          </div>

          <form class="reply" @submit.prevent="sendReply">
            <textarea
              v-model="reply.body"
              class="reply-body"
              rows="3"
              :placeholder="`回复 ${activeTitle}（送回发件方地址，仍在同一对话里）`"
              data-test="mail-reply-body"
            ></textarea>
            <div class="compose-row">
              <label class="urgent-pick"><input v-model="reply.urgent" type="checkbox" /> 急件</label>
              <button class="act act--primary mono" type="submit" :disabled="replying || !!replyWriteBlock" :title="replyWriteBlock || undefined" data-test="mail-reply">
                {{ replying ? '发送中…' : '回复' }}
              </button>
            </div>
            <p v-if="replyError" class="err" data-test="mail-reply-error">{{ replyError }}</p>
            <p v-if="readdressError" class="err" data-test="mail-readdress-error">{{ readdressError }}</p>
          </form>
        </template>
        <p v-else class="dim pad">左边选一个对话；要发新信件点右上角「写信」。</p>
      </section>
    </div>

    <SessionDrawer v-if="openSid" :sid="openSid" @close="openSid = ''" @changed="load(true)" />
  </section>
</template>

<style scoped>
.mail-page {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}
.mail-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.mail-title {
  font-size: 16px;
  margin: 0;
  color: var(--paper);
}
.mail-tabs {
  display: flex;
  gap: 6px;
}
.tab {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  padding: 3px 10px;
  font-size: 12px;
}
.tab--active {
  color: var(--ink);
  background: var(--phosphor);
  border-color: var(--phosphor);
}
.filter-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 8px;
  font-size: 11px;
  color: var(--queue);
}
.chip-x {
  background: none;
  border: 0;
  color: var(--fail);
  font-size: 14px;
  line-height: 1;
  padding: 0 2px;
}
.compose,
.reply {
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px;
}
.compose-to,
.readdress input {
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--paper);
  padding: 6px 8px;
  font-size: 12px;
}
.compose-body,
.reply-body {
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--paper);
  padding: 8px;
  font-size: 13px;
  font-family: var(--font-sans);
  resize: vertical;
}
.compose-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.urgent-pick {
  color: var(--queue);
  font-size: 12px;
}
.split {
  display: flex;
  gap: 12px;
  align-items: stretch;
  min-width: 0;
}
.convs {
  flex: 0 0 340px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 70vh;
  overflow-y: auto;
}
.conv {
  display: flex;
  flex-direction: column;
  gap: 3px;
  text-align: left;
  background: var(--panel);
  border: 1px solid var(--line);
  border-left: 2px solid transparent;
  border-radius: var(--radius);
  padding: 8px 10px;
  color: var(--paper);
}
.conv--active {
  border-left-color: var(--phosphor);
}
.conv-people {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.conv-arrow {
  font-size: 11px;
}
.conv-excerpt,
.conv-meta {
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.detail {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.detail-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  border-bottom: 1px dashed var(--line);
  padding-bottom: 8px;
}
.back {
  display: none;
}
.timeline {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 62vh;
  overflow-y: auto;
  padding-right: 4px;
}
/* 气泡：自己的一方在右（--right），对方在左；回信用虚线边与左侧细线区分新信。 */
.bubble {
  max-width: 76%;
  background: var(--panel);
  border: 1px solid var(--line);
  border-left: 2px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  transition: outline-color 0.2s;
}
.bubble--left {
  align-self: flex-start;
}
.bubble--right {
  align-self: flex-end;
  border-left-color: var(--run);
  background: var(--term-bg);
}
.bubble--reply {
  border-style: dashed;
}
.bubble--urgent {
  border-right: 2px solid var(--fail);
}
.bubble--hot {
  outline: 2px solid var(--phosphor);
  outline-offset: 1px;
}
.bubble-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 11px;
  color: var(--paper);
}
.bubble-to {
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 24ch;
  white-space: nowrap;
}
.reply-ref {
  background: none;
  border: 1px dashed var(--line);
  border-radius: 999px;
  color: var(--phosphor);
  font-size: 10px;
  padding: 0 6px;
}
.bubble-body {
  margin-top: 6px;
  font-size: 13px;
  color: var(--paper);
}
.fold {
  margin-top: 4px;
  background: none;
  border: 0;
  color: var(--phosphor);
  font-size: 11px;
  padding: 0;
}
.held {
  margin-top: 10px;
  border-top: 1px dashed var(--line);
  padding-top: 8px;
}
.held-why {
  margin: 0 0 6px;
  font-size: 12px;
  color: var(--queue);
}
.cands {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
  font-size: 12px;
}
.cand {
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--phosphor);
  padding: 2px 8px;
  font-size: 11px;
}
.readdress {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.readdress input {
  flex: 1 1 240px;
}
.tag {
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 0 5px;
  font-size: 10px;
}
.tag--urgent {
  color: var(--fail);
}
.unread {
  min-width: 16px;
  border-radius: 999px;
  background: var(--phosphor);
  color: var(--ink);
  font-size: 10px;
  text-align: center;
  padding: 1px 5px;
}
.tone--run {
  color: var(--run);
}
.tone--done {
  color: var(--done);
}
.tone--fail {
  color: var(--fail);
}
.tone--queue {
  color: var(--queue);
}
.act {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--paper);
  padding: 4px 10px;
  font-size: 12px;
}
.act--primary {
  background: var(--phosphor);
  border-color: var(--phosphor);
  color: var(--ink);
  font-weight: 600;
}
.err {
  color: var(--fail);
  font-size: 12px;
  margin: 0;
}
.notice {
  color: var(--done);
  font-size: 12px;
  margin: 0;
}
.dim {
  color: var(--queue);
}
.pad {
  padding: 8px 0;
  font-size: 12px;
}

/* 窄屏：列表与时间线切换显示（沿用 900px 断点）；窄屏下气泡占满宽度。 */
@media (max-width: 900px) {
  .split {
    flex-direction: column;
  }
  .convs {
    flex: 1 1 auto;
    max-height: none;
  }
  .split:not(.split--detail) .detail {
    display: none;
  }
  .split--detail .convs {
    display: none;
  }
  .back {
    display: inline-flex;
  }
  .timeline {
    max-height: none;
  }
  .bubble {
    max-width: 100%;
  }
}
.held-item {
  margin-left: 0.5em;
}
</style>
