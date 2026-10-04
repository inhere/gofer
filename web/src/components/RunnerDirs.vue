<script setup lang="ts">
// runner 的“工作目录”折叠区：默认工作空间 → roots 映射 → 各项目在该 runner 上的解析路径。
// 不存在的目录标红；解析不了的项目（policy_rejected）带原因标红。
// 旧版 worker 没有上报目录时只显示一行“未上报”，不猜。
import { computed } from 'vue'
import type { Runner } from '../api/types'

const props = defineProps<{ runner: Runner }>()

const dirs = computed(() => props.runner.dirs)
const rejected = computed(() => props.runner.worker?.policy_rejected ?? [])
const reported = computed(() => !!dirs.value)
const missingCount = computed(() => {
  const d = dirs.value
  if (!d) return 0
  return (d.workspace && !d.workspace.exists ? 1 : 0)
    + (d.roots ?? []).filter((r) => !r.exists).length
    + (d.projects ?? []).filter((p) => !p.exists).length
    + rejected.value.length
})
</script>

<template>
  <details class="rdirs" data-test="runner-dirs">
    <summary class="rdirs-sum mono">
      工作目录
      <span v-if="missingCount > 0" class="rdirs-bad" data-test="dirs-problem">{{ missingCount }} 处有问题</span>
      <span v-else-if="!reported" class="rdirs-dim">未上报</span>
    </summary>
    <p v-if="!reported" class="rdirs-note mono">该 runner 没有上报目录（旧版本 worker，或还没有心跳）。升级 worker 后这里会显示。</p>
    <template v-else>
      <div v-if="dirs?.workspace" class="rdirs-line mono" data-test="dirs-workspace">
        <span class="rdirs-k">默认工作空间</span>
        <code :class="{ 'rdirs-missing': !dirs.workspace.exists }">{{ dirs.workspace.path }}</code>
        <span v-if="!dirs.workspace.exists" class="rdirs-bad">不存在：可 mkdir 创建，或用 GOFER_WORKSPACE 指向别的目录</span>
      </div>
      <div v-if="dirs?.roots?.length" class="rdirs-block">
        <div class="rdirs-k mono">roots 映射（server 路径 → 本机路径）</div>
        <div v-for="r in dirs.roots" :key="r.from" class="rdirs-line mono">
          <code>{{ r.from }}</code><span class="rdirs-arrow">→</span>
          <code :class="{ 'rdirs-missing': !r.exists }">{{ r.to }}</code>
          <span v-if="!r.exists" class="rdirs-bad">目录不存在</span>
        </div>
      </div>
      <div v-if="dirs?.projects?.length || rejected.length" class="rdirs-block">
        <div class="rdirs-k mono">项目解析路径</div>
        <div v-for="p in dirs?.projects ?? []" :key="p.key" class="rdirs-line mono">
          <span class="rdirs-key">{{ p.key }}</span>
          <code :class="{ 'rdirs-missing': !p.exists }">{{ p.path }}</code>
          <span v-if="!p.exists" class="rdirs-bad">目录不存在</span>
        </div>
        <div v-for="x in rejected" :key="x.key" class="rdirs-line mono" data-test="dirs-rejected">
          <span class="rdirs-key">{{ x.key }}</span>
          <span class="rdirs-bad">无法解析：{{ x.reason }}</span>
        </div>
      </div>
    </template>
  </details>
</template>

<style scoped>
.rdirs { margin-top: 8px; font-size: 11px; }
.rdirs-sum { cursor: pointer; color: var(--queue); }
.rdirs-sum:hover { color: var(--paper); }
.rdirs-bad { color: var(--fail); margin-left: 8px; }
.rdirs-dim { color: var(--queue); margin-left: 8px; opacity: 0.7; }
.rdirs-note { margin: 6px 0 0; color: var(--queue); }
.rdirs-block { margin-top: 8px; }
.rdirs-k { color: var(--queue); margin-right: 8px; }
.rdirs-line { margin-top: 4px; color: var(--paper); min-width: 0; line-height: 1.6; word-break: break-all; }
.rdirs-line > * + * { margin-left: 8px; }
.rdirs-line code { color: var(--phosphor); word-break: break-all; }
.rdirs-key { color: var(--paper); font-weight: 600; }
.rdirs-arrow { color: var(--line); }
.rdirs-missing { color: var(--fail) !important; text-decoration: line-through dotted; }
</style>
