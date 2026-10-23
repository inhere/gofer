<script setup lang="ts">
// MCP-06：MCP 注册表（config.yaml 顶层 mcp_servers）。只读：增删改走配置文件，改完
// 「重新读取文件」即生效。secret 只显示变量名和 server 本机是否已设置；「测试」在 server
// 本机启动 / 连接该 MCP server，握手并列出工具（worker 上的 job 读 worker 自己的环境，
// 到那台机器上用 `gofer agent mcp test <name>` 测）。
// UI 重设计 P5-d：FormSection 分节 + 文案走 i18n（settings.mcp.*）。
import { onMounted, ref } from 'vue'
import { listMCPServers, testMCPServer } from '../../api/client'
import type { MCPProbeResp, MCPServerView } from '../../api/types'
import { t } from '../../i18n'
import FormSection from '../../ui/FormSection.vue'
import { mcpBoundText, mcpMissingSecrets, mcpTarget } from '../../utils/mcpView'

const servers = ref<MCPServerView[]>([])
const error = ref('')
const loading = ref(false)
const loaded = ref(false)
const probing = ref<Record<string, boolean>>({})
const probes = ref<Record<string, MCPProbeResp>>({})

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    servers.value = await listMCPServers()
    loaded.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function probe(name: string): Promise<void> {
  probing.value = { ...probing.value, [name]: true }
  try {
    const r = await testMCPServer(name)
    probes.value = { ...probes.value, [name]: r }
  } catch (e) {
    probes.value = { ...probes.value, [name]: { ok: false, error: e instanceof Error ? e.message : String(e) } }
  } finally {
    probing.value = { ...probing.value, [name]: false }
  }
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="mcp">
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.mcp.title') }}</h1>
        <p class="sub">{{ t('settings.mcp.sub') }}</p>
      </div>
      <button class="btn" type="button" :disabled="loading" @click="load()">
        {{ loading ? t('settings.mcp.refreshing') : t('act.refresh') }}
      </button>
    </header>

    <p class="note">
      {{ t('settings.mcp.introBefore') }} <code>mcp_servers</code>
      {{ t('settings.mcp.introMiddle') }} <code>server.mcp</code> / <code>agents.&lt;k&gt;.mcp</code> /
      <code>projects.&lt;k&gt;.mcp</code> / <code>job run --mcp</code> {{ t('settings.mcp.introAfter') }}
    </p>

    <p v-if="error" class="msg msg--err" :title="error">
      {{ t('settings.mcp.loadFailed', { error }) }}
    </p>
    <p v-else-if="loaded && servers.length === 0" class="msg" data-testid="mcp-empty">
      {{ t('settings.mcp.empty') }}
    </p>

    <FormSection
      v-for="s in servers"
      :key="s.name"
      :title="s.name"
      :desc="s.spec.desc || undefined"
    >
      <div class="card-head">
        <span class="tag mono">{{ s.spec.type || 'stdio' }}</span>
        <span v-if="!s.enabled" class="tag tag--off">{{ t('settings.mcp.disabled') }}</span>
        <span class="spacer" />
        <button class="btn" type="button" :disabled="probing[s.name]" @click="probe(s.name)">
          {{ probing[s.name] ? t('settings.mcp.testing') : t('settings.mcp.test') }}
        </button>
      </div>

      <dl class="grid">
        <dt>{{ (s.spec.type || 'stdio') === 'http' ? 'URL' : t('settings.mcp.command') }}</dt>
        <dd class="brk mono">{{ mcpTarget(s) }}</dd>
        <template v-if="s.secrets.length > 0">
          <dt>secret</dt>
          <dd class="secrets mono">
            <span v-for="sec in s.secrets" :key="sec.usage" class="secret" :class="{ missing: !sec.set }">
              {{ sec.usage }} ← ${{ sec.env }}{{ sec.set ? '' : t('settings.mcp.secretUnset') }}
            </span>
          </dd>
        </template>
        <template v-if="s.spec.tools && s.spec.tools.length > 0">
          <dt>{{ t('settings.mcp.tools') }}</dt>
          <dd class="brk mono">{{ s.spec.tools.join(', ') }}</dd>
        </template>
        <dt>{{ t('settings.mcp.bound') }}</dt>
        <dd class="brk mono">{{ mcpBoundText(s) || t('settings.mcp.unbound') }}</dd>
      </dl>

      <p v-if="mcpMissingSecrets(s).length > 0" class="warn">
        {{ t('settings.mcp.missingSecrets', { names: mcpMissingSecrets(s).join(', ') }) }}
      </p>

      <div
        v-if="probes[s.name]"
        class="probe mono"
        :class="probes[s.name].ok ? 'probe--ok' : 'probe--fail'"
        data-testid="mcp-probe"
      >
        <template v-if="probes[s.name].ok && probes[s.name].result">
          {{
            t('settings.mcp.connected', {
              name: probes[s.name].result?.server_name || '—',
              version: probes[s.name].result?.server_version || '',
              ms: probes[s.name].result?.elapsed_ms ?? 0,
            })
          }}
          <div class="brk">
            {{
              t('settings.mcp.toolList', {
                n: probes[s.name].result?.tools.length ?? 0,
                tools: probes[s.name].result?.tools.join(', ') || t('settings.mcp.noTools'),
              })
            }}
          </div>
        </template>
        <template v-else>{{ t('settings.mcp.failed', { error: probes[s.name].error || '' }) }}</template>
      </div>
    </FormSection>
  </div>
</template>

<style scoped>
.mcp {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
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
  color: var(--fg);
}
.sub {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--fg-3);
}

.note {
  font-size: var(--fs-sm);
  color: var(--fg-3);
  margin: 0;
  line-height: 1.6;
}
.note code {
  color: var(--fg-2);
}

.card-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.tag {
  font-size: var(--fs-xs);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 6px;
  color: var(--fg-2);
}
.tag--off {
  color: var(--fail);
  border-color: var(--fail);
}
.spacer {
  flex: 1;
}

.grid {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  gap: 6px 12px;
  margin: 10px 0 0;
  font-size: var(--fs-sm);
}
.grid dt {
  color: var(--fg-3);
}
.grid dd {
  margin: 0;
  color: var(--fg);
  min-width: 0;
}
.secrets {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.secret.missing {
  color: var(--fail);
}
.brk {
  word-break: break-all;
}

.warn {
  margin: 10px 0 0;
  font-size: var(--fs-sm);
  color: var(--fail);
}

.probe {
  margin-top: 10px;
  font-size: var(--fs-sm);
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  border: 1px solid var(--line);
}
.probe--ok {
  color: var(--accent);
  border-color: var(--accent);
}
.probe--fail {
  color: var(--fail);
  border-color: var(--fail);
  word-break: break-word;
}

.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 6px 12px;
  font: inherit;
  font-size: var(--fs-sm);
  background: transparent;
  color: var(--fg);
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
  color: var(--fg-2);
}
.msg--err {
  color: var(--fail);
  border-color: var(--fail);
  word-break: break-word;
}

@media (max-width: 480px) {
  .grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .page-head {
    flex-direction: column;
  }
}
</style>
