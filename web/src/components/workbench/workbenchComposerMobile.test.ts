import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const composer = readFileSync(resolve(__dirname, 'WorkbenchComposer.vue'), 'utf8')
const workbench = readFileSync(resolve(__dirname, '../../views/Workbench.vue'), 'utf8')

describe('mobile session launcher placement', () => {
  it('keeps the floating launcher away from an open session and puts a launcher in its top bar', () => {
    expect(composer).toContain('v-if="!open && !sessionOpen"')
    expect(composer).toContain('defineProps<{ sessionOpen: boolean }>()')
    expect(workbench).toContain('class="session-new mono"')
    expect(workbench).toContain('v-if="mobilePane === \'main\'"')
  })
})
