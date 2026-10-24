// 接入页 gofer-x5k4：「在此浏览器记住登录」与地址不一致的提示。
//
// 项目里没有 DOM 测试环境（只有 SSR），所以：存储语义在 store/authStorage.test.ts 覆盖，
// 地址比对抽成纯函数在这里逐条覆盖，页面本身用 SSR 结构与源码断言（复选框默认勾选、连接时
// 带上勾选状态、提示的字典 key 与 data-testid）。
import { describe, expect, it, vi } from 'vitest'
import { access as accessDict } from '../i18n/pages/access'
import { settings as settingsDict } from '../i18n/pages/settings'
import { mismatchedNotifyOrigin } from './accessOrigin'

// 页面 setup 时不会拉接口（onMounted 在 SSR 下不跑），但导入链里有 client，给个空实现即可。
vi.mock('../api/client', () => ({
  getHealth: vi.fn(async () => ({ ok: true, service: 'gofer', server_time: 0 })),
  listProjects: vi.fn(async () => ({ projects: [] })),
}))

const raw = (glob: Record<string, unknown>) => Object.values(glob)[0] as string
// 页面里有 <img src="/…png">，node 下 SSR 渲染会去解析这个公共资源 URL 而报错（其他页面测试
// 都不渲染带图片的模板），所以这里只用源码断言结构；行为语义在 store 与纯函数测试里。
const accessSrc = raw(import.meta.glob('./Access.vue', { eager: true, query: '?raw', import: 'default' }))
const aboutSrc = raw(import.meta.glob('./settings/About.vue', { eager: true, query: '?raw', import: 'default' }))
const authSrc = raw(import.meta.glob('../store/auth.ts', { eager: true, query: '?raw', import: 'default' }))
const clientSrc = raw(import.meta.glob('../api/client.ts', { eager: true, query: '?raw', import: 'default' }))

describe('notify origin mismatch', () => {
  it('reports the notification origin when it differs from the address in use', () => {
    expect(mismatchedNotifyOrigin('http://localhost:8765', 'http://192.168.0.10:8765')).toBe('http://192.168.0.10:8765')
    // 路径 / 末尾斜杠不影响比对：比的是 origin。
    expect(mismatchedNotifyOrigin('http://localhost:8765', 'http://192.168.0.10:8765/jobs/abc')).toBe(
      'http://192.168.0.10:8765',
    )
    // 协议或端口不同也算不同的 origin。
    expect(mismatchedNotifyOrigin('http://localhost:8765', 'https://localhost:8765')).toBe('https://localhost:8765')
  })

  it('stays quiet when the addresses agree, are unset or are not URLs', () => {
    expect(mismatchedNotifyOrigin('http://localhost:8765', 'http://localhost:8765/')).toBe('')
    expect(mismatchedNotifyOrigin('http://localhost:8765', undefined)).toBe('')
    expect(mismatchedNotifyOrigin('http://localhost:8765', '   ')).toBe('')
    expect(mismatchedNotifyOrigin('http://localhost:8765', 'not a url')).toBe('')
  })
})

describe('access page', () => {
  it('asks to remember the sign-in in this browser, checked by default', () => {
    expect(accessSrc).toMatch(/data-testid="access-remember"/)
    expect(accessSrc).toMatch(/<input v-model="remember" type="checkbox" data-testid="access-remember" \/>/)
    expect(accessSrc).toMatch(/const remember = ref\(true\)/)
    expect(accessSrc).toMatch(/\{\{ t\('access\.remember'\) \}\}/)
    expect(accessSrc).toContain('data-testid="access-token"')
    expect(accessSrc).toContain('data-testid="access-connect"')
  })

  it('renders the origin notice only when there is something to say', () => {
    expect(accessSrc).toMatch(/const notifyOrigin = ref\(''\)/)
    expect(accessSrc).toMatch(/<p v-if="notifyOrigin" class="notice" data-testid="access-origin-hint">/)
  })

  it('stores the token according to the checkbox', () => {
    expect(accessSrc).toMatch(/const remember = ref\(true\)/)
    expect(accessSrc).toMatch(/setToken\(token, \{ remember: remember\.value \}\)/)
    // 登录失败 / 401 兜底清两处存储。
    expect(accessSrc).toMatch(/clearToken\(\)/)
  })

  it('compares the current origin with the address the server puts in notifications', () => {
    // /health 免鉴权，接入页（还没有 token）也能拉。
    expect(clientSrc).toMatch(/export function getHealth\(\): Promise<HealthResp> \{\s*return request<HealthResp>\('\/health'\)/)
    expect(accessSrc).toMatch(/getHealth\(\)/)
    expect(accessSrc).toMatch(/mismatchedNotifyOrigin\(window\.location\.origin, health\.web_base_url\)/)
    expect(accessSrc).toMatch(/data-testid="access-origin-hint"/)
    expect(accessSrc).toMatch(/t\('access\.originMismatch', \{ origin: notifyOrigin \}\)/)
    // 拿不到探针结果时不提示，也不影响登录。
    expect(accessSrc).toMatch(/catch \{[\s\S]*?\}/)
  })

  it('has both dictionaries for the new keys', () => {
    for (const key of ['access.remember', 'access.rememberHint', 'access.originMismatch']) {
      expect(accessDict.zh[key], `${key} 缺中文`).toBeTypeOf('string')
      expect(accessDict.en[key], `${key} 缺英文`).toBeTypeOf('string')
    }
    // 旧提示说「只存在 sessionStorage」，勾选后就不成立了。
    expect(accessDict.zh['access.hint']).not.toContain('sessionStorage')
  })
})

describe('settings about: sign out and forget this browser', () => {
  it('offers the action next to what this browser keeps', () => {
    expect(aboutSrc).toMatch(/data-testid="about-logout-forget"/)
    expect(aboutSrc).toMatch(/t\('settings\.about\.logoutForget'\)/)
    expect(aboutSrc).toMatch(/data-testid="about-remember-state"/)
    expect(aboutSrc).toMatch(/remembered \? t\('settings\.about\.remembered'\) : t\('settings\.about\.notRemembered'\)/)
  })

  it('clears both storages and goes back to the sign-in page', () => {
    expect(aboutSrc).toMatch(/import \{ clearToken, isRemembered \} from '\.\.\/\.\.\/store\/auth'/)
    expect(aboutSrc).toMatch(/function logoutForget\(\): void \{\s*clearToken\(\)\s*void router\.replace\('\/access'\)/)
    // clearToken 清两处：记住登录的那份也要没。
    expect(authSrc).toMatch(/export function clearToken\(\): void \{\s*removeToken\(sessionStore\(\)\)\s*removeToken\(localStore\(\)\)/)
  })

  it('has both dictionaries for the new keys', () => {
    for (const key of [
      'settings.about.browser',
      'settings.about.browserDesc',
      'settings.about.remembered',
      'settings.about.notRemembered',
      'settings.about.logoutForget',
      'settings.about.logoutForgetHint',
    ]) {
      expect(settingsDict.zh[key], `${key} 缺中文`).toBeTypeOf('string')
      expect(settingsDict.en[key], `${key} 缺英文`).toBeTypeOf('string')
    }
  })
})
