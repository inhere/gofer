import { describe, expect, it } from 'vitest'
import { translate } from '../i18n'
import { DIR_LOCK_HINT, formatLockPaths, parseLockPaths } from './lockPaths'

describe('lock paths input (gofer-jkd2)', () => {
  it('splits on newlines and commas and trims the entries', () => {
    expect(parseLockPaths('gookit/ini\ngookit/cli')).toEqual(['gookit/ini', 'gookit/cli'])
    expect(parseLockPaths('gookit/ini, gookit/cli ')).toEqual(['gookit/ini', 'gookit/cli'])
  })

  it('treats blank input as "no declared lock"', () => {
    expect(parseLockPaths('')).toEqual([])
    expect(parseLockPaths('  \n , \n')).toEqual([])
  })

  it('keeps a project-relative path as typed (the server resolves and validates it)', () => {
    expect(parseLockPaths(' sub/mod \n')).toEqual(['sub/mod'])
  })

  it('formats a prefill back into one path per line', () => {
    expect(formatLockPaths(['gookit/ini', 'gookit/cli'])).toBe('gookit/ini\ngookit/cli')
    expect(formatLockPaths([])).toBe('')
    expect(formatLockPaths(undefined)).toBe('')
  })

  it('hints the same rule everywhere the field appears', () => {
    expect(translate('util.lock.hint', undefined, 'zh-CN')).toContain('留空 = 锁整个 CWD')
    expect(DIR_LOCK_HINT.value).toBe(translate('util.lock.hint'))
  })
})
