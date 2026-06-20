<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { listProjects } from '../api/client'
import { clearToken, setToken } from '../store/auth'
import ThemeToggle from '../components/ThemeToggle.vue'

const router = useRouter()

const tokenInput = ref('')
const loading = ref(false)
const error = ref('')

async function connect() {
  const t = tokenInput.value.trim()
  if (!t) {
    error.value = '请输入 token'
    return
  }
  error.value = ''
  loading.value = true
  setToken(t)
  try {
    // 用 listProjects 验证 token 与服务连通性
    await listProjects()
    router.replace({ path: '/board' })
  } catch (e) {
    // 401 时 client 已清 token；其他失败这里兜底清掉
    clearToken()
    const detail = e instanceof Error ? e.message : String(e)
    error.value = `token 无效或服务不可达（${detail}）`
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="access">
    <div class="access-toolbar">
      <ThemeToggle />
    </div>
    <div class="card">
      <div class="card-head mono">
        <span class="card-name">Gofer</span>
        <span class="card-sep">&#9656;</span>
        <span class="card-sub">access</span>
      </div>
      <p class="hint">粘贴访问 token 以接入控制台。token 仅保存在当前会话（sessionStorage）。</p>

      <label class="label mono" for="token-input">BEARER TOKEN</label>
      <textarea
        id="token-input"
        v-model="tokenInput"
        class="token-input mono"
        rows="3"
        placeholder="粘贴 token..."
        spellcheck="false"
        autocomplete="off"
        @keydown.ctrl.enter="connect"
      ></textarea>

      <button class="connect" type="button" :disabled="loading" @click="connect">
        {{ loading ? '连接中...' : '连接' }}
      </button>

      <p v-if="error" class="error mono">{{ error }}</p>
    </div>
  </div>
</template>

<style scoped>
.access {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 70vh;
}

/* 登录页无顶栏：主题切换放页面右上角 */
.access-toolbar {
  position: absolute;
  top: 0;
  right: 0;
}

.card {
  width: 100%;
  max-width: 440px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 28px;
}

.card-head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 16px;
  letter-spacing: 0.04em;
  margin-bottom: 4px;
}
.card-name {
  color: var(--paper);
  font-weight: 600;
}
.card-sep {
  color: var(--phosphor);
}
.card-sub {
  color: var(--queue);
}

.hint {
  color: var(--queue);
  font-size: 13px;
  margin: 8px 0 20px;
}

.label {
  display: block;
  font-size: 11px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin-bottom: 6px;
}

.token-input {
  width: 100%;
  background: var(--ink);
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px;
  font-size: 13px;
  resize: vertical;
  outline: none;
}
.token-input:focus {
  border-color: var(--phosphor);
}

.connect {
  margin-top: 16px;
  width: 100%;
  background: var(--phosphor);
  color: var(--ink);
  border: none;
  border-radius: var(--radius);
  padding: 10px;
  font-size: 14px;
  font-weight: 600;
}
.connect:disabled {
  opacity: 0.55;
  cursor: default;
}

.error {
  margin-top: 14px;
  color: var(--fail);
  font-size: 12px;
  word-break: break-word;
}
</style>
