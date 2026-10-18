<script setup lang="ts">
// 信箱页（design 2026-10-16「二、gofer 信箱」，计划 M7）：一个信箱一个页面——
//  - 左：对话串列表（按最近活动倒序；对方、状态、急件、未读）+ 页签过滤（全部 / 收件 /
//    发件 / 待指派）；
//  - 右：串内按时间显示每封信（出处 → 收件地址、状态、正文**按纯文本**渲染）+ 底部回复框；
//  - 待指派：原因、候选（服务端在发信 / 改投的回答里带回）、改投（改地址重发）、撤回；
//  - 顶部「写信」：地址与急件开关。
// 规则（地址解析校验、串分组、状态文案、查询参数）都在 utils/mail.ts，这里只管渲染与请求。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ackMail,
  cancelMail,
  listMail,
  readdressMail,
  replyMail,
  sendMail,
} from '../api/client'
import { fmtAgo, fmtDateTime } from '../api/time'
import type { MailCandidate, MailMessage } from '../api/types'
import { useWhoami } from '../store/whoami'
import {
  buildMailReaddress,
  buildMailReply,
  buildMailSend,
  groupMailThreads,
  MAIL_FILTERS,
  mailExcerpt,
  mailFilterFromQuery,
  mailListQuery,
  mailOutgoing,
  mailPartyLabel,
  mailReplyTarget,
  mailStateLabel,
  mailStateTone,
  mailUnread,
  type MailFilter,
  type MailIdentity,
} from '../utils/mail'

const route = useRoute()
const router = useRouter()
const { whoami } = useWhoami()

const mails = ref<MailMessage[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
const nowSec = ref(Math.floor(Date.now() / 1000))
const activeThreadId = ref('')

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
// 点进来，要把它发的信认成发件）。
const me = computed<MailIdentity>(() => ({ caller: whoami.value?.caller_id ?? '', session: sessionFilter.value }))

const threads = computed(() => groupMailThreads(mails.value, me.value))
const active = computed(() => threads.value.find((t) => t.threadId === activeThreadId.value) ?? null)

async function load(silent = false): Promise<void> {
  if (!silent) loading.value = true
  nowSec.value = Math.floor(Date.now() / 1000)
  try {
    const q = mailListQuery(filter.value, { session: sessionFilter.value })
    const resp = await listMail(q)
    mails.value = resp.mail ?? []
    error.value = ''
    if (!threads.value.some((t) => t.threadId === activeThreadId.value)) {
      activeThreadId.value = threads.value[0]?.threadId ?? ''
    }
  } catch (e) {
    mails.value = []
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
    void load()
  },
)

// 打开一个串 = 读过它的信：把自己收的未读信件逐封 ack（读 = 送达；服务端只对收件方生效）。
async function selectThread(threadId: string): Promise<void> {
  activeThreadId.value = threadId
  replyError.value = ''
  notice.value = ''
  const t = threads.value.find((x) => x.threadId === threadId)
  if (!t) return
  const unread = t.mails.filter((m) => mailUnread(m) && !mailOutgoing(m, me.value))
  if (!unread.length) return
  try {
    await Promise.all(unread.map((m) => ackMail(m.id)))
    await load(true)
  } catch {
    // 读信失败不影响看信：状态保持原样，服务端仍是权威。
  }
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
  const t = active.value
  if (!t) return
  const built = buildMailReply(reply.value.body, reply.value.urgent)
  replyError.value = built.error
  if (!built.req) return
  replying.value = true
  try {
    // 回复送回原发件方地址（不是我发出的那封的最新一封）。
    const resp = await replyMail(mailReplyTarget(t, me.value).id, built.req)
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

onMounted(() => {
  void load()
})
</script>

<template>
  <section class="mail-page">
    <header class="mail-head">
      <h1 class="mail-title">信箱</h1>
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
        placeholder="正文（纯文本，单封上限 32 KB）"
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

    <div class="split">
      <aside class="threads" aria-label="对话串">
        <p v-if="loading" class="dim pad">加载中…</p>
        <p v-else-if="!threads.length" class="dim pad" data-test="mail-empty">没有信件</p>
        <button
          v-for="t in threads"
          :key="t.threadId"
          class="thread"
          :class="{ 'thread--active': t.threadId === activeThreadId }"
          type="button"
          data-test="mail-thread"
          @click="selectThread(t.threadId)"
        >
          <span class="thread-top">
            <span class="thread-peer">{{ t.peer }}</span>
            <span v-if="t.urgent" class="tag tag--urgent" data-test="mail-thread-urgent">急件</span>
            <span v-if="t.unread" class="unread" data-test="mail-thread-unread">{{ t.unread }}</span>
            <span class="thread-state" :class="`tone--${mailStateTone(t.state)}`">{{ t.stateLabel }}</span>
          </span>
          <span class="thread-excerpt dim">{{ mailExcerpt(t.latest.body, 80) }}</span>
          <span class="thread-meta dim mono">{{ t.mails.length }} 封 · {{ fmtAgo(t.activityAt, nowSec) }}</span>
        </button>
      </aside>

      <section class="detail" aria-label="对话串详情">
        <template v-if="active">
          <article
            v-for="m in active.mails"
            :key="m.id"
            class="letter"
            :class="{ 'letter--mine': mailOutgoing(m, me) }"
            data-test="mail-letter"
          >
            <header class="letter-head mono">
              <span class="letter-from">{{ mailPartyLabel(m.from) }}</span>
              <span class="dim">→</span>
              <span class="letter-to">{{ m.to_address || mailPartyLabel(m.to) }}</span>
              <span v-if="m.urgent" class="tag tag--urgent">急件</span>
              <span class="letter-state" :class="`tone--${mailStateTone(m.state)}`" data-test="mail-letter-state">
                {{ mailStateLabel(m.state) }}
              </span>
              <span class="dim" :title="fmtDateTime(m.created_at)">#{{ m.id }} · {{ fmtAgo(m.created_at, nowSec) }}</span>
            </header>
            <!-- 正文一律纯文本插值（不渲染 HTML）：信件内容来自别的 agent 会话。 -->
            <p class="letter-body" data-test="mail-letter-body">{{ m.body }}</p>

            <div v-if="m.state === 'held'" class="held" data-test="mail-held">
              <p class="held-why">
                <b>待指派</b>
                <span v-if="m.last_error" class="dim">· {{ m.last_error }}</span>
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

          <form class="reply" @submit.prevent="sendReply">
            <textarea
              v-model="reply.body"
              class="reply-body"
              rows="3"
              :placeholder="`回复 ${active.peer}（送回发件方地址，仍在同一对话串）`"
              data-test="mail-reply-body"
            ></textarea>
            <div class="compose-row">
              <label class="urgent-pick"><input v-model="reply.urgent" type="checkbox" /> 急件</label>
              <button class="act act--primary mono" type="submit" :disabled="replying" data-test="mail-reply">
                {{ replying ? '发送中…' : '回复' }}
              </button>
            </div>
            <p v-if="replyError" class="err" data-test="mail-reply-error">{{ replyError }}</p>
            <p v-if="readdressError" class="err" data-test="mail-readdress-error">{{ readdressError }}</p>
          </form>
        </template>
        <p v-else class="dim pad">左边选一个对话串；要发新信件点右上角「写信」。</p>
      </section>
    </div>
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
.threads {
  flex: 0 0 320px;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 70vh;
  overflow-y: auto;
}
.thread {
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
.thread--active {
  border-left-color: var(--phosphor);
}
.thread-top {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.thread-peer {
  font-size: 13px;
  font-weight: 600;
}
.thread-excerpt,
.thread-meta {
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.thread-state {
  font-size: 11px;
}
.detail {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.letter {
  background: var(--panel);
  border: 1px solid var(--line);
  border-left: 2px solid var(--line);
  border-radius: var(--radius);
  padding: 10px;
}
.letter--mine {
  border-left-color: var(--run);
}
.letter-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 11px;
  color: var(--paper);
}
.letter-body {
  margin: 8px 0 0;
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 13px;
  color: var(--paper);
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

/* 窄屏：左列表在上、详情在下，列表不再固定宽度。 */
@media (max-width: 900px) {
  .split {
    flex-direction: column;
  }
  .threads {
    flex: 1 1 auto;
    max-height: 40vh;
  }
}
</style>
