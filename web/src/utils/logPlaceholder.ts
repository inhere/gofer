// A live append can arrive after the empty state was rendered into the same <pre>.
import { t } from '../i18n'

export function appendRenderedLog(
  pre: Pick<HTMLElement, 'textContent' | 'insertAdjacentHTML'>,
  html: string,
  stream: 'stdout' | 'stderr',
): void {
  if (pre.textContent === t('util.log.emptyStream', { stream })) pre.textContent = ''
  pre.insertAdjacentHTML('beforeend', html)
}
