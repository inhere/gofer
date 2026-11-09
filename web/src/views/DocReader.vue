<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ApiError, getProjectDoc } from '../api/client'
import type { ProjectDoc } from '../api/types'
import MarkdownBlock from '../components/MarkdownBlock.vue'
import { t } from '../i18n'

interface Heading { id: string; level: number; text: string }

const route = useRoute()
const doc = ref<ProjectDoc | null>(null)
const loading = ref(false)
const errorStatus = ref(0)
const errorText = ref('')
const copied = ref(false)
const headings = ref<Heading[]>([])
const contentEl = ref<HTMLElement | null>(null)

const projectKey = computed(() => String(route.params.key ?? ''))
const path = computed(() => String(route.query.path ?? ''))
const base = computed(() => String(route.query.base ?? ''))
const line = computed(() => {
  const value = Number(route.query.line)
  return Number.isInteger(value) && value > 0 ? value : 0
})
const anchor = computed(() => String(route.query.anchor ?? ''))
const worktreeBranch = computed(() => String(route.query.wtb ?? ''))
const docDir = computed(() => {
  const value = doc.value?.path ?? ''
  const index = value.lastIndexOf('/')
  return index >= 0 ? value.slice(0, index) : ''
})

function errorMessage(status: number, withWorktree: boolean): string {
  if (status === 409) return t('docReader.error.remote')
  if (status === 403) return t('docReader.error.forbidden')
  if (status === 415) return t('docReader.error.binary')
  if (status === 400) return t('docReader.error.missing')
  if (withWorktree) return t('docReader.error.missingWorktree', { branch: worktreeBranch.value })
  return t('docReader.error.missing')
}

async function load(): Promise<void> {
  doc.value = null
  headings.value = []
  errorStatus.value = 0
  errorText.value = ''
  copied.value = false
  if (!projectKey.value || !path.value) {
    errorStatus.value = 400
    errorText.value = errorMessage(400, false)
    return
  }
  loading.value = true
  try {
    doc.value = await getProjectDoc(projectKey.value, path.value, base.value || undefined)
    await nextTick()
    decorateHeadings()
  } catch (error) {
    errorStatus.value = error instanceof ApiError ? error.status : 0
    errorText.value = errorMessage(errorStatus.value, !!worktreeBranch.value)
  } finally {
    loading.value = false
  }
}

function decorateHeadings(): void {
  const root = contentEl.value
  if (!root) return
  const result: Heading[] = []
  root.querySelectorAll<HTMLElement>('h1, h2, h3').forEach((heading, index) => {
    const id = `doc-h-${index + 1}`
    heading.id = id
    result.push({ id, level: Number(heading.tagName.slice(1)), text: heading.textContent ?? '' })
  })
  headings.value = result
  if (anchor.value) {
    const wanted = anchor.value.trim().toLocaleLowerCase()
    const match = result.find((item) => item.text.trim().toLocaleLowerCase() === wanted)
    if (match) document.getElementById(match.id)?.scrollIntoView({ block: 'start' })
  }
}

function readerUrl(): string {
  return window.location.href
}

async function copyPath(): Promise<void> {
  try {
    await navigator.clipboard.writeText(readerUrl())
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1500)
  } catch {
    copied.value = false
  }
}

watch(() => route.fullPath, () => void load())
onMounted(() => void load())
</script>

<template>
  <main class="page doc-reader" data-test="doc-reader">
    <header class="doc-head">
      <div>
        <p class="doc-project mono">{{ projectKey }}</p>
        <h1 class="doc-path mono">{{ doc?.path || path }}</h1>
        <p v-if="line" class="doc-line">{{ t('docReader.line', { line }) }}</p>
        <p v-if="doc?.truncated" class="doc-warning">{{ t('docReader.truncated') }}</p>
      </div>
      <button v-if="doc" class="btn" type="button" data-test="copy-doc-path" @click="copyPath">{{ copied ? t('docReader.copied') : t('docReader.copy') }}</button>
    </header>

    <p v-if="loading" class="doc-state">{{ t('docReader.loading') }}</p>
    <section v-else-if="errorText" class="doc-error" role="alert" data-test="doc-error">
      <strong>{{ errorText }}</strong>
      <p v-if="errorStatus">{{ t('docReader.error.status', { status: errorStatus }) }}</p>
    </section>
    <div v-else class="doc-layout">
      <article ref="contentEl" class="doc-content">
        <MarkdownBlock
          v-if="doc"
          :text="doc.content"
          :doc-context="{ projectKey, worktreeBranch: worktreeBranch || undefined }"
          :doc-dir="docDir"
        />
      </article>
      <nav v-if="headings.length" class="doc-toc" :aria-label="t('docReader.toc')">
        <strong>{{ t('docReader.toc') }}</strong>
        <a v-for="heading in headings" :key="heading.id" :href="`#${heading.id}`" :class="`doc-toc-l${heading.level}`">{{ heading.text }}</a>
      </nav>
    </div>
  </main>
</template>

<style scoped>
.doc-reader { max-width: 1180px; }
.doc-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; margin-bottom: 18px; }
.doc-project { margin: 0 0 4px; color: var(--accent); }
.doc-path { margin: 0; overflow-wrap: anywhere; }
.doc-line, .doc-warning { color: var(--ink-3); margin: 6px 0 0; }
.doc-warning { color: var(--run); }
.doc-state, .doc-error { padding: 22px; border: 1px solid var(--line); }
.doc-error { color: var(--fail); }
.doc-error p { color: var(--ink-3); }
.doc-layout { display: grid; grid-template-columns: minmax(0, 1fr) 220px; gap: 28px; align-items: start; }
.doc-content { min-width: 0; }
.doc-toc { position: sticky; top: 16px; display: flex; flex-direction: column; gap: 6px; border-left: 1px solid var(--line); padding-left: 14px; font-size: 12px; }
.doc-toc a { color: var(--ink-3); text-decoration: none; }
.doc-toc a:hover { color: var(--accent); }
.doc-toc-l2 { padding-left: 10px; }
.doc-toc-l3 { padding-left: 20px; }
@media (max-width: 760px) {
  .doc-layout { display: flex; flex-direction: column; }
  .doc-toc { position: static; order: -1; width: 100%; border-left: 0; border-bottom: 1px solid var(--line); padding: 0 0 10px; }
}
</style>
