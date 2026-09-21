// web 终端（AttachTerminal）的焦点归属口径（h-aii-vwux）：终端只在 **xterm 宿主
// 内部**有事件/焦点时拦截全局粘贴与快捷键；焦点落在宿主之外的输入控件（组件自己的
// 会话发送框、页面其它输入框）时一律交还浏览器默认行为。
//
// 这里是纯函数、且只用"结构化"判断（tagName / isContentEditable / host.contains），
// 不依赖真实 DOM 类型，所以能在 node 里直接跑断言（web 无测试框架；用例见文件底部与
// tmp/ webpty 断言脚本）。

// isTextEntry 判定元素是否是"会吃掉粘贴/按键"的文本输入控件。用 tagName 而非
// instanceof（node 里没有 HTMLElement，浏览器里两者等价）。
export function isTextEntry(el: EventTarget | null): boolean {
  if (!el || typeof el !== 'object') {
    return false
  }
  const tag = (el as Element).tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || (el as HTMLElement).isContentEditable === true
}

// terminalOwnsEvent 回答"这次 document 级事件该由终端接管吗"：
//   - 事件目标或当前焦点在 xterm 宿主（host）内 → 是。xterm 自己的隐藏 textarea
//     就在宿主内，所以宿主判定必须先于"输入框让位"判定；
//   - 否则目标是/焦点在输入控件（含组件下方的发送框）→ 否；
//   - 其余情况（点了终端栏按钮、刚点过终端）沿用 terminalActive。
export function terminalOwnsEvent(
  target: EventTarget | null,
  activeElement: EventTarget | null,
  host: Node | null,
  terminalActive: boolean,
): boolean {
  if (!host) {
    return false
  }
  const inHost = (node: EventTarget | null): boolean =>
    node != null && typeof node === 'object' && host.contains(node as Node)
  if (inHost(target)) {
    return true
  }
  if (isTextEntry(target)) {
    return false
  }
  if (inHost(activeElement)) {
    return true
  }
  if (isTextEntry(activeElement)) {
    return false
  }
  return terminalActive
}

// 手测用例（浏览器里逐条过一遍；逻辑断言见汇报里的 node 输出）：
//  1. 终端内粘贴（焦点在 xterm）        → 文本进 pty，页面不插入任何内容；
//  2. 发送框内粘贴（Ctrl+V 或右键）      → 文本进发送框，pty 收不到字节；
//  3. 页面其它输入框（如搜索框）粘贴      → 文本进该输入框，pty 收不到字节；
//  4. 终端"发送键 → Ctrl+V"按钮点击      → 走 navigator.clipboard 送进 pty（与焦点无关）；
//  5. 只读端（无写租约）粘贴             → 任何位置都不写 pty（sendInput 被 writeGranted 挡住）。
