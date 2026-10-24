// 接入页的地址提示（gofer-x5k4）。
//
// 桌面通知 / 钉钉链接里的地址来自 server.web_base_url。用户平时可能用另一个地址（localhost
// 与局域网 IP）打开控制台：两者是不同 origin，"在此浏览器记住登录"的 token 各自一份，从通知
// 打开就会要求重新登录。接入页拿 /health 报告的 web_base_url 与当前 origin 比一比，不同就
// 提示一行，让用户自己发现地址不一致（配置问题，前端改不了）。
//
// 返回通知链接指向的 origin；地址相同、没配置或配置的不是合法 URL 时返回空串（不提示）。
export function mismatchedNotifyOrigin(currentOrigin: string, webBaseURL: string | undefined): string {
  const base = (webBaseURL ?? '').trim()
  if (!base) return ''
  try {
    const origin = new URL(base).origin
    return origin && origin !== currentOrigin ? origin : ''
  } catch {
    return ''
  }
}
