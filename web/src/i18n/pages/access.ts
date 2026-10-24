// access 页的文案（UI 重设计 P5；「记住登录」与地址提示为 gofer-x5k4）。每个页面一个文件、
// zh 与 en 并列；key 前缀 `access.`，命名规范见设计稿 §9.2。
import type { PageDict } from './types'

export const access: PageDict = {
  zh: {
    'access.title': '接入控制台',
    'access.brandAria': '在 GitHub 查看 Gofer 仓库',
    'access.hint': '粘贴访问 token 以接入控制台。',
    'access.tokenLabel': '访问令牌',
    'access.tokenPlaceholder': '输入 token…',
    'access.remember': '在此浏览器记住登录',
    'access.rememberHint':
      '勾选时 token 存在这个浏览器（按站点保存），重开浏览器或从桌面通知打开都不用再登录；不勾选只留在当前标签页，关掉标签页即失效。',
    'access.connect': '连接',
    'access.connecting': '连接中…',
    'access.empty': '请输入 token',
    'access.invalid': 'token 无效或服务不可达（{detail}）',
    'access.originMismatch':
      '通知链接指向 {origin}，与当前地址不同：从通知打开会要求重新登录。要免登录，用同一个地址打开控制台，或把 server.web_base_url 改成这个地址。',
  },
  en: {
    'access.title': 'Connect to the console',
    'access.brandAria': 'Open the Gofer repository on GitHub',
    'access.hint': 'Paste an access token to connect to the console.',
    'access.tokenLabel': 'Access token',
    'access.tokenPlaceholder': 'Paste token…',
    'access.remember': 'Remember me in this browser',
    'access.rememberHint':
      'Checked: the token is kept in this browser (per site), so reopening the browser or opening a desktop-notification link needs no sign-in. Unchecked: it stays in this tab only and is gone when the tab closes.',
    'access.connect': 'Connect',
    'access.connecting': 'Connecting…',
    'access.empty': 'Enter a token',
    'access.invalid': 'Invalid token or unreachable server ({detail})',
    'access.originMismatch':
      'Notification links point to {origin}, not this address, so opening one will ask you to sign in again. To skip that, open the console at the same address, or set server.web_base_url to this address.',
  },
}
