// 字节数的人类可读渲染（Dashboard 的 db 体积卡与 /settings/tunnels 的隧道流量共用）。
//
// 1024 进制、单位 B/KB/MB/GB/TB；小于 10 的值保留一位小数（"1.5 MB" 比 "2 MB" 有用），
// 非法/非正数一律 "0 B"。
export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) {
    return '0 B'
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
