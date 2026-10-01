// A live append can arrive after the empty state was rendered into the same <pre>.
export function appendRenderedLog(
  pre: Pick<HTMLElement, 'textContent' | 'insertAdjacentHTML'>,
  html: string,
  stream: 'stdout' | 'stderr',
): void {
  if (pre.textContent === `（无 ${stream} 输出）`) pre.textContent = ''
  pre.insertAdjacentHTML('beforeend', html)
}
