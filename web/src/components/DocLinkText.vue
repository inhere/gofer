<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { docReaderLocation, findDocPaths, type DocContext } from '../utils/docPath'

const props = defineProps<{ text: string; docContext?: DocContext }>()

function chunks(): Array<{ text: string; ref?: ReturnType<typeof findDocPaths>[number] }> {
  if (!props.docContext) return [{ text: props.text }]
  const matches = findDocPaths(props.text)
  const out: Array<{ text: string; ref?: ReturnType<typeof findDocPaths>[number] }> = []
  let cursor = 0
  for (const match of matches) {
    if (match.start > cursor) out.push({ text: props.text.slice(cursor, match.start) })
    out.push({ text: props.text.slice(match.start, match.end), ref: match })
    cursor = match.end
  }
  if (cursor < props.text.length) out.push({ text: props.text.slice(cursor) })
  return out
}
</script>

<template>
  <span class="doc-link-text">
    <template v-for="(chunk, index) in chunks()" :key="index">
      <RouterLink v-if="chunk.ref && docContext" :to="docReaderLocation(docContext, chunk.ref)">{{ chunk.text }}</RouterLink>
      <span v-else>{{ chunk.text }}</span>
    </template>
  </span>
</template>
