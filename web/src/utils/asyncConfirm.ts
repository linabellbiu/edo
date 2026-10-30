import { Modal, type ModalFuncProps } from 'ant-design-vue'

interface AsyncConfirmOptions extends Omit<ModalFuncProps, 'onOk' | 'onCancel' | 'afterClose'> {
  action: (signal: AbortSignal) => Promise<void>
  isCurrent: () => boolean
  onError: (error: unknown) => void
  onCancel?: () => void
  afterClose?: () => void
}

export function asyncConfirm({ action, isCurrent, onError, onCancel, afterClose, ...options }: AsyncConfirmOptions) {
  const controller = new AbortController()
  let finished = false, submitting = false
  const current = () => !finished && !controller.signal.aborted && isCurrent()
  const destroy = () => { finished = true; controller.abort(); dialog.destroy() }
  const dialog = Modal.confirm({
    ...options,
    onCancel(close?: () => void) {
      if (submitting || finished) return
      finished = true
      controller.abort()
      onCancel?.()
      close?.()
    },
    afterClose() { finished = true; controller.abort(); afterClose?.() },
    // Ant 会把返回的拒绝 Promise 再次抛出；回调模式由本处负责关闭及错误反馈。
    onOk(close: () => void) {
      if (submitting || finished) return
      if (!current()) { destroy(); return }
      submitting = true
      dialog.update({ okButtonProps: { ...options.okButtonProps, loading: true, disabled: true }, cancelButtonProps: { ...options.cancelButtonProps, disabled: true }, keyboard: false })
      void (async () => {
        try {
          await action(controller.signal)
          if (current()) { finished = true; close() }
        } catch (error) {
          if (current()) onError(error)
        } finally {
          submitting = false
          if (current()) dialog.update({ okButtonProps: { ...options.okButtonProps, loading: false }, cancelButtonProps: options.cancelButtonProps || { disabled: false }, keyboard: options.keyboard ?? true })
          else if (!finished) destroy()
        }
      })()
    },
  })
  return { ...dialog, destroy }
}
