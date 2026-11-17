import type { ObjectDirective } from 'vue'
import { message } from 'ant-design-vue'

// 使用浏览器标准 Clipboard/Selection API；内网 HTTP 使用当前选区复制并核对结果。
// 已调研 VueUse useClipboard：其 legacy 分支忽略 execCommand 返回值，且临时文本框会替换选区。
export async function selectAndCopyFullText(element: HTMLElement): Promise<boolean> {
  const document = element.ownerDocument
  const text = element.textContent || ''
  if (!text || text === '—') return false
  const selection = document.getSelection()
  if (selection) {
    const range = document.createRange()
    range.selectNodeContents(element)
    selection.removeAllRanges()
    selection.addRange(range)
  }
  try {
    let clipboardError: unknown
    const clipboard = document.defaultView?.navigator.clipboard
    if (clipboard?.writeText) {
      try {
        await clipboard.writeText(text)
        return true
      } catch (err) {
        clipboardError = err
        // 部分内网环境拒绝 Clipboard API，继续使用用户刚刚选中的原文。
      }
    }
    if (selection?.toString() === text && document.execCommand('copy')) return true
    throw clipboardError || new Error('浏览器未完成复制')
  } catch (err) {
    console.warn('复制完整文本失败', { operation: 'copy_full_text', err })
    return false
  }
}

const cleanups = new WeakMap<HTMLElement, () => void>()

export const vCopyFullText: ObjectDirective<HTMLElement> = {
  mounted(element) {
    const previousTabindex = element.getAttribute('tabindex')
    const previousShortcuts = element.getAttribute('aria-keyshortcuts')
    if (!element.matches('button,a[href],input,textarea,select,[tabindex]')) element.tabIndex = 0
    element.setAttribute('aria-keyshortcuts', 'Control+C Meta+C')
    element.classList.add('copy-full-text')
    const copy = async (event: Event) => {
      if (!element.textContent || element.textContent === '—') return
      event.preventDefault()
      event.stopPropagation()
      const copied = await selectAndCopyFullText(element)
      if (copied) message.success({ key: 'copy-full-text', content: '已复制完整内容', duration: 1.5 })
      else message.warning({ key: 'copy-full-text', content: '自动复制失败，请按 Ctrl/Cmd+C 复制已选中的完整内容' })
    }
    const keydown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'c') {
        // 已有手动选区时保留浏览器原本的局部复制行为。
        if (element.ownerDocument.getSelection()?.toString()) return
        void copy(event)
      }
    }
    element.addEventListener('dblclick', copy)
    element.addEventListener('keydown', keydown)
    cleanups.set(element, () => {
      element.removeEventListener('dblclick', copy)
      element.removeEventListener('keydown', keydown)
      element.classList.remove('copy-full-text')
      if (previousTabindex === null) element.removeAttribute('tabindex')
      else element.setAttribute('tabindex', previousTabindex)
      if (previousShortcuts === null) element.removeAttribute('aria-keyshortcuts')
      else element.setAttribute('aria-keyshortcuts', previousShortcuts)
    })
  },
  beforeUnmount(element) {
    cleanups.get(element)?.()
    cleanups.delete(element)
  },
}
