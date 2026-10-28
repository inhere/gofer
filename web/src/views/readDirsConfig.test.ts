import { describe, expect, it } from 'vitest'
import { settings as settingsDict } from '../i18n/pages/settings'
import { projects as projectsDict } from '../i18n/pages/projects'

const raw = (glob: Record<string, unknown>): string => Object.values(glob)[0] as string
const configSrc = raw(import.meta.glob('./Config.vue', { eager: true, query: '?raw', import: 'default' }))
const projectsSrc = raw(import.meta.glob('./Projects.vue', { eager: true, query: '?raw', import: 'default' }))

const dicts = [settingsDict.zh, settingsDict.en]

describe('read directory configuration wiring', () => {
  it('renders and sends cli-agent add_dir_args, while hiding it for acp-agent', () => {
    expect(configSrc).toContain("agentForm.type === 'cli-agent' && agentPolicy['add_dir_args']?.editable")
    expect(configSrc).toContain("body.add_dir_args = agentForm.type === 'cli-agent' ? lines(agentForm.addDirArgsText) : []")
    expect(configSrc).toContain("arg.includes('{{dir}}')")
    expect(configSrc).toContain("t('settings.config.errAddDirArgs')")
    for (const dict of dicts) {
      expect(dict['settings.config.f.addDirArgs']).toBeTruthy()
      expect(dict['settings.config.errAddDirArgs']).toBeTruthy()
    }
  })

  it('renders and sends the server read_dir_allow list', () => {
    expect(configSrc).toContain('read_dir_allow: lines(serverForm.readDirAllowText)')
    expect(configSrc).toContain('v-model="serverForm.readDirAllowText"')
    expect(configSrc).toContain("t('settings.config.f.readDirAllow')")
    for (const dict of dicts) {
      expect(dict['settings.config.f.readDirAllow']).toBeTruthy()
      expect(dict['settings.config.hintReadDirAllow']).toBeTruthy()
    }
  })

  it('renders and sends project read_dirs', () => {
    expect(projectsSrc).toContain("read_dirs: form.readDirsText")
    expect(projectsSrc).toContain('v-model="form.readDirsText"')
    expect(projectsSrc).toContain("t('projects.field.readDirs')")
    expect(projectsDict.zh['projects.field.readDirs']).toBeTruthy()
    expect(projectsDict.en['projects.field.readDirs']).toBeTruthy()
  })
})
