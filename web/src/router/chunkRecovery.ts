import { Modal } from 'ant-design-vue'
import type { Router } from 'vue-router'

const recoveryKey = 'edo:chunk-recovery'
const loadErrorPattern = /Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed|Unable to preload CSS for/i

// 只处理 Vite 确认的资源加载错误，组件执行异常仍交给原来的错误处理。
export function installChunkRecovery(router: Router) {
  const preloadErrors = new WeakSet<object>()
  const handledErrors = new WeakSet<object>()
  const entry = document.querySelector<HTMLScriptElement>('script[type="module"][src]')?.src
  let reloading = false
  let retryDialog: ReturnType<typeof Modal.confirm> | undefined

  window.addEventListener('vite:preloadError', event => {
    const error = (event as Event & { payload: unknown }).payload
    if (error instanceof Error && loadErrorPattern.test(error.message)) preloadErrors.add(error)
    // 保留异常，让 Router 得到本次导航的准确目标，并先执行离开页面的检查。
  })

  router.beforeEach(() => {
    reloading = false
    retryDialog?.destroy()
    retryDialog = undefined
  })

  router.onError((error, to) => {
    if (!preloadErrors.has(error)) return
    handledErrors.add(error)
    console.warn('[EDO] 页面资源加载失败', { path: to.path, error })

    const target = new URL(router.resolve(to).href, window.location.href)
    if (target.origin !== window.location.origin || reloading) return

    const reload = () => {
      reloading = true
      // 完整导航继续触发原生 beforeunload，未保存的内容仍可取消离开。
      if (target.href === window.location.href) window.location.reload()
      else window.location.assign(target.href)
    }

    let canReload = false
    try {
      // 每个页面版本在当前标签内最多自动恢复一次，网络或发布故障不会导致刷新循环。
      if (entry && window.sessionStorage.getItem(recoveryKey) !== entry) {
        window.sessionStorage.setItem(recoveryKey, entry)
        canReload = true
      }
    } catch (storageError) {
      console.warn('[EDO] 无法保存页面恢复状态', storageError)
    }
    if (canReload) {
      reload()
      return
    }

    const dialog = Modal.confirm({
      title: '页面加载失败',
      content: '页面资源未能加载，请检查网络后重新加载。当前页面有未保存的内容时，请先保存。',
      okText: '重新加载',
      cancelText: '留在当前页',
      maskClosable: false,
      onOk: reload,
      afterClose: () => { if (retryDialog === dialog) retryDialog = undefined },
    })
    retryDialog = dialog
  })

  window.addEventListener('unhandledrejection', event => {
    // 菜单的 void router.push 仍会拒绝；仅消除已提供恢复入口的同一个资源错误。
    if (event.reason instanceof Error && handledErrors.has(event.reason)) event.preventDefault()
  })
}
