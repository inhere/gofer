// access 页的文案（UI 重设计 P5）。每个页面一个文件、zh 与 en 并列；key 前缀 `access.`，
// 命名规范见设计稿 §9.2。
import type { PageDict } from './types'

export const access: PageDict = {
  zh: {
    'access.title': '接入控制台',
    'access.brandAria': '在 GitHub 查看 Gofer 仓库',
    'access.hint': '粘贴访问 token 以接入控制台。token 只保存在当前会话（sessionStorage）。',
    'access.tokenLabel': '访问令牌',
    'access.tokenPlaceholder': '输入 token…',
    'access.connect': '连接',
    'access.connecting': '连接中…',
    'access.empty': '请输入 token',
    'access.invalid': 'token 无效或服务不可达（{detail}）',
  },
  en: {
    'access.title': 'Connect to the console',
    'access.brandAria': 'Open the Gofer repository on GitHub',
    'access.hint': 'Paste an access token to connect. The token is kept in this session only (sessionStorage).',
    'access.tokenLabel': 'Access token',
    'access.tokenPlaceholder': 'Paste token…',
    'access.connect': 'Connect',
    'access.connecting': 'Connecting…',
    'access.empty': 'Enter a token',
    'access.invalid': 'Invalid token or unreachable server ({detail})',
  },
}
