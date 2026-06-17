// 纯 Bearer 无状态鉴权。token 存 sessionStorage。

const TOKEN_KEY = 'agent-bridge-token'

let unauthorizedHandler: (() => void) | null = null

export function getToken(): string | null {
  return sessionStorage.getItem(TOKEN_KEY)
}

export function setToken(token: string): void {
  sessionStorage.setItem(TOKEN_KEY, token)
}

export function clearToken(): void {
  sessionStorage.removeItem(TOKEN_KEY)
}

export function hasToken(): boolean {
  const t = getToken()
  return t != null && t.length > 0
}

// 注册 401 处理回调（main.ts 注册：清 token + 跳 /access）
export function setUnauthorizedHandler(fn: () => void): void {
  unauthorizedHandler = fn
}

// client 在收到 401 时调用：清 token 并触发回调
export function triggerUnauthorized(): void {
  clearToken()
  if (unauthorizedHandler) {
    unauthorizedHandler()
  }
}
