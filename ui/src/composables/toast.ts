import { reactive } from 'vue'

export interface Toast {
  id: number
  kind: 'ok' | 'error' | 'info'
  text: string
}

export const toasts = reactive<Toast[]>([])
let seq = 0

export function toast(text: string, kind: Toast['kind'] = 'ok', ms = 3500) {
  const id = ++seq
  toasts.push({ id, kind, text })
  setTimeout(() => dismiss(id), ms)
}

export function toastError(e: unknown) {
  toast(e instanceof Error ? e.message : String(e), 'error', 6000)
}

export function dismiss(id: number) {
  const i = toasts.findIndex((t) => t.id === id)
  if (i >= 0) toasts.splice(i, 1)
}
