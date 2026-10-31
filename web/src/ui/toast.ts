// Toast 的纯函数部分：自动消失的时长口径。组件本身在 Toast.vue。
import { isMobileWidth, toastDwellMs } from '../utils/mobileLayout'

// 提示条自动消失的停留时间（毫秒）：手机档（压在会话内容上）几秒后自己收掉，
// 宽屏返回 0 = 不自动收，沿用「等用户点关闭」。取不到窗口（SSR）按宽屏算。
export function toastDuration(): number {
  const mobile =
    typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? isMobileWidth(window.innerWidth) : false
  return toastDwellMs(mobile)
}
