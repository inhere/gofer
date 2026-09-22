// 全局轮询的统一节奏（F8）：后台标签页、多显示器下「窗口在后台但页面可见」的窗口都不该
// 继续刷 API——用户看到的就是「不在那个页面却一直在请求」。
//
// createPoller(fn, intervalMs) 返回 { start, stop }：
//   - start() 立刻拉一次并起定时；
//   - document.hidden（切走标签页）或窗口 blur（切到别的窗口）时暂停定时器；
//   - visibilitychange / focus 恢复时立刻拉一次再起定时（回来就是最新状态）；
//   - 页面卸载（pagehide）时停表，pageshow（bfcache 回退）恢复；
//   - stop() 清定时器并摘掉全部监听（组件 onUnmounted 调）。
//
// 初始视为聚焦：不信任 document.hasFocus()——无头/自动化环境可能恒为 false，那样轮询
// 永远起不来；只有真收到 blur 才置为失焦，focus 事件恢复。
export interface Poller {
  start(): void
  stop(): void
}

export function createPoller(fn: () => void | Promise<void>, intervalMs: number): Poller {
  let timer: number | null = null
  let started = false
  let focused = true

  function tick(): void {
    void Promise.resolve()
      .then(fn)
      .catch(() => {
        // 轮询失败不打断页面；下一轮继续。
      })
  }

  function resume(): void {
    if (timer != null || !started || document.hidden || !focused) {
      return
    }
    tick()
    timer = window.setInterval(tick, intervalMs)
  }

  function suspend(): void {
    if (timer != null) {
      window.clearInterval(timer)
      timer = null
    }
  }

  // 事件回调（不是一次性小包装）：add/removeEventListener 需要同一函数身份。
  function onVisibility(): void {
    if (document.hidden || !focused) {
      suspend()
    } else {
      resume()
    }
  }

  function onBlur(): void {
    focused = false
    suspend()
  }

  function onFocus(): void {
    focused = true
    resume()
  }

  return {
    start(): void {
      if (started) {
        return
      }
      started = true
      document.addEventListener('visibilitychange', onVisibility)
      window.addEventListener('blur', onBlur)
      window.addEventListener('focus', onFocus)
      window.addEventListener('pagehide', suspend)
      window.addEventListener('pageshow', onVisibility)
      resume()
    },
    stop(): void {
      if (!started) {
        return
      }
      started = false
      suspend()
      document.removeEventListener('visibilitychange', onVisibility)
      window.removeEventListener('blur', onBlur)
      window.removeEventListener('focus', onFocus)
      window.removeEventListener('pagehide', suspend)
      window.removeEventListener('pageshow', onVisibility)
    },
  }
}
