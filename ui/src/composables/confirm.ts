import { reactive } from 'vue'

export interface ConfirmOptions {
  title: string
  body: string
  confirm?: string
  danger?: boolean
}

export const confirmState = reactive({
  open: false,
  opts: { title: '', body: '' } as ConfirmOptions,
  resolve: null as ((v: boolean) => void) | null,
})

export function confirmDialog(opts: ConfirmOptions): Promise<boolean> {
  confirmState.resolve?.(false)
  confirmState.opts = opts
  confirmState.open = true
  return new Promise((r) => (confirmState.resolve = r))
}

export function settleConfirm(v: boolean) {
  confirmState.open = false
  confirmState.resolve?.(v)
  confirmState.resolve = null
}
