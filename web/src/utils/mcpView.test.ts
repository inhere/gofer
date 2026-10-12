import { describe, expect, it } from 'vitest'
import { mcpBoundText, mcpMissingSecrets, mcpTarget } from './mcpView'
import type { MCPServerView } from '../api/types'

const base: MCPServerView = {
  name: 'db',
  enabled: true,
  spec: { command: 'npx', args: ['-y', 'server-pg'] },
  secrets: [
    { env: 'A', set: true, usage: 'env:URL' },
    { env: 'B', set: false, usage: 'env:PW' },
  ],
  bound: { server: false, agents: ['claude'], projects: ['web'] },
}

describe('mcpView', () => {
  it('describes bindings, target and missing secrets', () => {
    expect(mcpBoundText(base)).toBe('agent:claude，project:web')
    expect(mcpBoundText({ ...base, bound: { server: true, agents: [], projects: [] } })).toBe('server（所有 job）')
    expect(mcpBoundText({ ...base, bound: { server: false, agents: [], projects: [] } })).toBe('')
    expect(mcpTarget(base)).toBe('npx -y server-pg')
    expect(mcpTarget({ ...base, spec: { type: 'http', url: 'https://x/mcp' } })).toBe('https://x/mcp')
    expect(mcpMissingSecrets(base)).toEqual(['B'])
  })
})
