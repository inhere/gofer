import { describe, expect, it } from 'vitest'

const source = Object.values(import.meta.glob('./Runners.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('Runners mobile topology', () => {
  it('collapses topology by default on phone widths', () => {
    expect(source).toContain("window.matchMedia('(max-width: 640px)')")
    expect(source).toContain('topologyOpen.value = false')
    expect(source).toContain('.topology-group details[open]')
  })

  it('styles the add-worker form with the shared field/label/control classes', () => {
    expect(source).toContain('class="field"')
    expect(source).toContain('class="control mono"')
    expect(source).toContain('.add-form .control')
    expect(source).toContain('.add-actions')
  })
})

const dirsSource = Object.values(import.meta.glob('../components/RunnerDirs.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('RunnerDirs other-worker projects', () => {
  it('folds projects that only other workers run into one grey line and never counts them as problems', () => {
    expect(dirsSource).toContain('dirs?.other_projects')
    expect(dirsSource).toContain('仅在其他 worker 运行')
    expect(dirsSource).toContain('data-test="dirs-other"')
    const missing = dirsSource.slice(dirsSource.indexOf('const missingCount'), dirsSource.indexOf('</script>'))
    expect(missing).not.toContain('other_projects')
  })
})
