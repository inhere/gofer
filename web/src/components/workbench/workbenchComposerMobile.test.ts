import { describe, expect, it } from 'vitest'

const composer = Object.values(import.meta.glob('./WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const workbench = Object.values(import.meta.glob('../../views/Workbench.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('mobile session launcher placement', () => {
  it('keeps the floating launcher away from an open session and puts a launcher in its top bar', () => {
    expect(composer).toContain('v-if="!open && !sessionOpen"')
    expect(composer).toContain('defineProps<{ sessionOpen: boolean }>()')
    expect(workbench).toContain('class="session-new mono"')
    expect(workbench).toContain('v-if="mobilePane === \'main\'"')
  })
})
