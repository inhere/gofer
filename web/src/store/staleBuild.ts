// 版本陈旧提示（F8）：服务端升级后，浏览器手里的旧 shell / 旧 chunk 已经取不到
// （hash 名字变了），页面看着还在却点不动。两处置位，都不另起定时器：
//  1) EscalationBell 那一轮既有的 /v1/stats 轮询发现 server version 与首屏记录的不一致
//     （服务端换了版本，当前页面还是旧构建）；
//  2) main.ts 捕获 chunk 加载失败但被 60s 冷却抑制（自动重载过一次仍失败）。
// 顶栏据此显示「有新版本，点击刷新」，点击即 location.reload()。
import { ref } from 'vue'

export const staleBuild = ref(false)

// 首屏第一次见到的 server version：此后每次不同即说明服务端换过版本。
let firstVersion: string | null = null

export function markStaleBuild(): void {
  staleBuild.value = true
}

export function noteServerVersion(version?: string): void {
  if (!version) {
    return
  }
  if (firstVersion === null) {
    firstVersion = version
    return
  }
  if (firstVersion !== version) {
    markStaleBuild()
  }
}
