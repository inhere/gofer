<script setup lang="ts">
// 接入页（无外壳，设计稿 §7.4）：只有 token 输入、「在此浏览器记住登录」、主题开关、语言开关。
// 行为：验证成功后回 ?redirect 指定的站内页面（默认 /dashboard）；跨标签页 token
// 交接在 router.ts 的公开路由守卫里（本页不参与）。
//
// gofer-x5k4：勾选「记住登录」（默认勾选）时 token 存 localStorage，按 origin 持久——从桌面
// 通知 / 钉钉链接打开的新标签页、重开浏览器都不用再登录；不勾选只存本标签页的 sessionStorage。
// 另外拉一次免鉴权的 /health：server 配了 web_base_url 且与当前 origin 不同时提示一行，
// 让用户发现「通知里的地址和自己在用的地址不是同一个」。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getHealth, listProjects, UnauthorizedError } from '../api/client'
import { clearToken, setToken } from '../store/auth'
import { t } from '../i18n'
import { REPO_URL } from '../utils/repo'
import ThemeToggle from '../components/ThemeToggle.vue'
import LangToggle from '../ui/LangToggle.vue'
import { mismatchedNotifyOrigin } from './accessOrigin'

const router = useRouter()

const tokenInput = ref('')
const remember = ref(true)
const loading = ref(false)
const error = ref('')
// 401（token 不对）时额外说明服务端 token 从哪来：服务方式运行时只读配置目录的
// .env / config.yaml，不读启动终端的环境变量（gofer-t9md）。
const unauthorized = ref(false)
// 通知链接指向的 origin（与当前地址不同时才有值）。
const notifyOrigin = ref('')

// 拉一次免鉴权的 /health，取 server 报告的 web_base_url 与当前 origin 比对。
async function loadNotifyOrigin(): Promise<void> {
  try {
    const health = await getHealth()
    notifyOrigin.value = mismatchedNotifyOrigin(window.location.origin, health.web_base_url)
  } catch {
    // 探针拉不到（老 server / 网络问题）就不提示，不影响登录
  }
}

onMounted(loadNotifyOrigin)

async function connect(): Promise<void> {
  const token = tokenInput.value.trim()
  if (!token) {
    error.value = t('access.empty')
    return
  }
  error.value = ''
  unauthorized.value = false
  loading.value = true
  setToken(token, { remember: remember.value })
  try {
    // 用 listProjects 验证 token 与服务连通性
    await listProjects()
    // 回到被送来登录前要打开的页面（只接受站内路径，防止跳到别的站点）
    const back = router.currentRoute.value.query.redirect
    const target =
      typeof back === 'string' && back.startsWith('/') && !back.startsWith('//') && !back.startsWith('/access')
        ? back
        : '/dashboard'
    void router.replace(target)
  } catch (e) {
    // 401 时 client 已清 token；其他失败这里兜底清掉
    clearToken()
    unauthorized.value = e instanceof UnauthorizedError
    const detail = e instanceof Error ? e.message : String(e)
    error.value = t('access.invalid', { detail })
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="access">
    <div class="access-toolbar">
      <ThemeToggle />
      <LangToggle />
    </div>

    <div class="card">
      <a
        class="brand-link"
        :href="REPO_URL"
        target="_blank"
        rel="noopener noreferrer"
        :aria-label="t('access.brandAria')"
      >
        <img class="brand-logo" src="/gofer-combination-400.png" alt="Gofer" />
      </a>

      <h1 class="title">{{ t('access.title') }}</h1>
      <p class="hint">{{ t('access.hint') }}</p>

      <form @submit.prevent="connect">
        <label class="label" for="token-input">{{ t('access.tokenLabel') }}</label>
        <input
          id="token-input"
          v-model="tokenInput"
          class="token-input mono"
          type="password"
          name="password"
          :placeholder="t('access.tokenPlaceholder')"
          spellcheck="false"
          autocomplete="current-password"
          autocapitalize="none"
          data-testid="access-token"
          required
        />

        <label class="remember">
          <input v-model="remember" type="checkbox" data-testid="access-remember" />
          <span>{{ t('access.remember') }}</span>
        </label>
        <p class="hint hint--sm">{{ t('access.rememberHint') }}</p>

        <button class="connect" type="submit" :disabled="loading" data-testid="access-connect">
          {{ loading ? t('access.connecting') : t('access.connect') }}
        </button>
      </form>

      <p v-if="notifyOrigin" class="notice" data-testid="access-origin-hint">
        {{ t('access.originMismatch', { origin: notifyOrigin }) }}
      </p>

      <p v-if="error" class="error" role="alert" data-testid="access-error">{{ error }}</p>
      <p v-if="unauthorized" class="notice" data-testid="access-token-source-hint">
        {{ t('access.tokenSourceHint') }}
      </p>
    </div>
  </div>
</template>

<style scoped>
.access {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  min-height: 100dvh;
  padding: 24px 16px;
  background: var(--bg);
}

/* 登录页没有外壳：主题与语言开关放右上角。 */
.access-toolbar {
  position: absolute;
  top: 12px;
  right: 12px;
  display: flex;
  gap: 8px;
  align-items: center;
}

.card {
  width: 100%;
  max-width: 420px;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
  box-shadow: var(--shadow-1);
  padding: 28px;
}

.brand-link {
  display: block;
  width: fit-content;
  margin: 0 auto 18px;
  border-radius: var(--r-panel);
  line-height: 0;
}
.brand-link:hover {
  text-decoration: none;
}
.brand-logo {
  display: block;
  width: min(260px, 100%);
  height: auto;
}

.title {
  margin: 0 0 6px;
  font-size: var(--fs-xl);
  color: var(--ink);
  text-align: center;
}

.hint {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  margin: 0 0 20px;
  text-align: center;
}
.hint--sm {
  margin: 6px 0 0;
  font-size: var(--fs-xs);
  text-align: left;
  line-height: 1.6;
}

.label {
  display: block;
  font-size: var(--fs-xs);
  font-weight: 500;
  color: var(--ink-2);
  margin-bottom: 6px;
}

.token-input {
  width: 100%;
  background: var(--sunken);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 10px 12px;
  font-size: var(--fs-md);
  outline: none;
}
.token-input:focus {
  border-color: var(--accent);
}

/* 「在此浏览器记住登录」：复选框紧跟一行文字，整行可点。 */
.remember {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 14px;
  font-size: var(--fs-sm);
  color: var(--ink);
  cursor: pointer;
}
.remember input {
  width: 16px;
  height: 16px;
  accent-color: var(--accent);
  cursor: pointer;
}

.connect {
  margin-top: 16px;
  width: 100%;
  min-height: 40px;
  background: var(--accent);
  color: var(--accent-ink);
  border: 1px solid var(--accent);
  border-radius: var(--r-ctl);
  padding: 10px;
  font-size: var(--fs-md);
  font-weight: 600;
}
.connect:hover:not(:disabled) {
  background: var(--accent-hover);
  border-color: var(--accent-hover);
}
.connect:disabled {
  opacity: 0.55;
  cursor: default;
}

/* 地址不一致的提示：黄色（需要人注意），不是错误。 */
.notice {
  margin: 16px 0 0;
  padding: 8px 10px;
  border: 1px solid var(--need);
  border-radius: var(--r-ctl);
  background: var(--need-soft);
  color: var(--need-ink);
  font-size: var(--fs-sm);
  line-height: 1.6;
  word-break: break-word;
}

.error {
  margin: 14px 0 0;
  color: var(--fail);
  font-size: var(--fs-sm);
  word-break: break-word;
}
</style>
