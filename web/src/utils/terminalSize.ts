// pty 尺寸同步的判定口径（h-aii-rx9a）。尺寸是共享真源（serve 广播 r 帧），
// 每一次"真的 resize"都会让 TUI 整屏重绘（实测 omp：一次 resize ≈ 11KB + 135 次
// 行清除），所以两处必须判等：
//   - 写者发帧：本地网格没变就不发；
//   - 收到权威尺寸：与自己刚发出的、或与本地已一致时不做任何本地 resize/fit。
// 抽成纯函数以便 node 断言（web 无测试框架）。

export interface Grid {
  cols: number
  rows: number
}

// FIT_DEBOUNCE_MS：ResizeObserver 驱动的 fit 合并窗口。窗口 resize 与容器尺寸
// 变化共用它，避免一帧内多次 fit → 多次 r 帧 → 多次整屏重绘。
export const FIT_DEBOUNCE_MS = 150

export function sameGrid(a: Grid | null | undefined, b: Grid | null | undefined): boolean {
  return !!a && !!b && a.cols === b.cols && a.rows === b.rows
}

// resizeFramePayload：写者本地网格变化时该发的 r 帧内容；与上次发出的相同则 null
// （服务端也会吞掉重复尺寸，这里只是不发无谓的帧）。
export function resizeFramePayload(prev: Grid | null, next: Grid): Grid | null {
  if (!prev) {
    return next
  }
  return sameGrid(prev, next) ? null : next
}

// sizeAction：收到 serve 权威尺寸（hello / r 帧）后本地该做什么。
//   - 'none'：什么都不做（写者收到自己 resize 的回声；只读端已与服务端一致）；
//   - 'fit' ：写者模式，本地视口是尺寸真源，重新 fit 后由 onResize 发 r 帧；
//   - 'resize'：只读跟随，按服务端尺寸 resize 本地 xterm（绝不 fit——软键盘/窗口
//     变化会把本地改小而与 pty 脱钩）。
export function sizeAction(opts: {
  write: boolean
  term: Grid
  server: Grid
  lastSent: Grid | null
}): 'none' | 'fit' | 'resize' {
  const { write, term, server, lastSent } = opts
  if (write) {
    return sameGrid(lastSent, server) ? 'none' : 'fit'
  }
  return sameGrid(term, server) ? 'none' : 'resize'
}
