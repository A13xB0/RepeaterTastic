import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

/** Track an element's content width (for pixel-exact SVG charts). */
export function useWidth(el: Ref<HTMLElement | null>, fallback = 600) {
  const width = ref(fallback)
  let ro: ResizeObserver | null = null
  onMounted(() => {
    if (!el.value) return
    width.value = el.value.clientWidth || fallback
    ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width
      if (w) width.value = w
    })
    ro.observe(el.value)
  })
  onBeforeUnmount(() => ro?.disconnect())
  return width
}

export function niceMax(v: number): number {
  if (v <= 0) return 1
  const p = 10 ** Math.floor(Math.log10(v))
  const n = v / p
  const m = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10
  return m * p
}

export function timeTick(t: number, spanMs: number): string {
  const d = new Date(t)
  if (spanMs > 3 * 86_400_000) return d.toLocaleDateString([], { weekday: 'short', day: 'numeric' })
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
}
