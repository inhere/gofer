<script setup lang="ts">
// MCP-06：MCP 注册表（config.yaml 顶层 mcp_servers）。只读：增删改走配置文件，改完
// 「重新读取文件」即生效。secret 只显示变量名和 server 本机是否已设置；「测试」在 server
// 本机启动 / 连接该 MCP server，握手并列出工具（worker 上的 job 读 worker 自己的环境，
// 到那台机器上用 `gofer agent mcp test <name>` 测）。
import { onMounted, ref } from 'vue'
import { listMCPServers, testMCPServer } from '../../api/client'
import type { MCPProbeResp, MCPServerView } from '../../api/types'
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
    <div class="head">
      <h2 class="title mono">MCP 注册表 / MCP SERVERS</h2>
      <button class="mini-btn mono" type="button" :disabled="loading" @click="load()">
        {{ loading ? '刷新中...' : '刷新' }}
      </button>
    </div>
    <p class="note mono">
      在 server 配置的 <code>mcp_servers</code> 里声明，用 <code>server.mcp</code> / <code>agents.&lt;k&gt;.mcp</code> /
      <code>projects.&lt;k&gt;.mcp</code> / <code>job run --mcp</code> 绑定。secret 只写环境变量名，值由执行 job 的机器提供。
    </p>
    <p v-if="error" class="error mono" :title="error">读取失败：{{ error }}</p>
    <p v-else-if="loaded && servers.length === 0" class="note mono">还没有声明任何 MCP server。</p>

    <div v-for="s in servers" :key="s.name" class="card">
      <div class="card-head">
        <span class="name mono">{{ s.name }}</span>
        <span class="tag mono">{{ s.spec.type || 'stdio' }}</span>
        <span v-if="!s.enabled" class="tag tag--off mono">已停用</span>
        <span class="spacer" />
        <button class="mini-btn mono" type="button" :disabled="probing[s.name]" @click="probe(s.name)">
          {{ probing[s.name] ? '测试中...' : '测试' }}
        </button>
      </div>
      <p v-if="s.spec.desc" class="desc">{{ s.spec.desc }}</p>
      <dl class="grid mono">
        <dt>{{ (s.spec.type || 'stdio') === 'http' ? 'URL' : '命令' }}</dt>
        <dd class="brk">{{ mcpTarget(s) }}</dd>
        <template v-if="s.secrets.length > 0">
          <dt>secret</dt>
          <dd class="secrets">
            <span v-for="sec in s.secrets" :key="sec.usage" class="secret" :class="{ missing: !sec.set }">
              {{ sec.usage }} ← ${{ sec.env }}{{ sec.set ? '' : '（server 未设置）' }}
            </span>
          </dd>
        </template>
        <template v-if="s.spec.tools && s.spec.tools.length > 0">
          <dt>工具白名单</dt>
          <dd class="brk">{{ s.spec.tools.join(', ') }}</dd>
        </template>
        <dt>绑定</dt>
        <dd class="brk">{{ mcpBoundText(s) || '未绑定（只有 job run --mcp 显式指定时才会用到）' }}</dd>
      </dl>
      <p v-if="mcpMissingSecrets(s).length > 0" class="warn mono">
        server 本机缺 {{ mcpMissingSecrets(s).join(', ') }}：在本机运行、绑定了它的 job 会被拒绝。
      </p>
      <div v-if="probes[s.name]" class="probe mono" :class="probes[s.name].ok ? 'probe--ok' : 'probe--fail'">
        <template v-if="probes[s.name].ok && probes[s.name].result">
          连接成功 · {{ probes[s.name].result?.server_name || '—' }} {{ probes[s.name].result?.server_version }}
          · {{ probes[s.name].result?.elapsed_ms }} ms
          <div class="brk">工具（{{ probes[s.name].result?.tools.length }}）：{{ probes[s.name].result?.tools.join(', ') || '无' }}</div>
        </template>
        <template v-else>失败：{{ probes[s.name].error }}</template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mcp {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0;
}
.note {
  font-size: 12px;
  color: var(--queue);
  margin: 0;
}
.card {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px 14px;
  min-width: 0;
}
.card-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.name {
  font-size: 13px;
  color: var(--paper);
}
.tag {
  font-size: 11px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 6px;
  color: var(--queue);
}
.tag--off {
  color: var(--fail);
  border-color: var(--fail);
}
.spacer {
  flex: 1;
}
.desc {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--paper);
}
.grid {
  display: grid;
  grid-template-columns: 90px minmax(0, 1fr);
  gap: 6px 12px;
  margin: 8px 0 0;
  font-size: 12px;
}
.grid dt {
  color: var(--queue);
}
.grid dd {
  margin: 0;
  color: var(--paper);
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
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--fail);
}
.probe {
  margin-top: 8px;
  font-size: 12px;
  border-radius: var(--radius);
  padding: 6px 10px;
  border: 1px solid var(--line);
}
.probe--ok {
  color: var(--done);
  border-color: var(--done);
}
.probe--fail {
  color: var(--fail);
  border-color: var(--fail);
  word-break: break-word;
}
.mini-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  cursor: pointer;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.mini-btn:disabled {
  color: var(--queue);
  cursor: default;
}
.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0;
  word-break: break-word;
}
@media (max-width: 480px) {
  .grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
