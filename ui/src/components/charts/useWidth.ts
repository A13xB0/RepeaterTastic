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

// Rounds a fraction up to the nearest "nice" multiplier (1, 2, 2.5, 5 or 10 of its power of ten).
const NICE_MULTIPLIERS: [limit: number, multiplier: number][] = [
  [1, 1],
  [2, 2],
  [2.5, 2.5],
  [5, 5],
]

function niceMultiplier(n: number): number {
  for (const [limit, multiplier] of NICE_MULTIPLIERS) if (n <= limit) return multiplier
  return 10
}

export function niceMax(v: number): number {
  if (v <= 0) return 1
  const p = 10 ** Math.floor(Math.log10(v))
  return niceMultiplier(v / p) * p
}

export function timeTick(t: number, spanMs: number): string {
  const d = new Date(t)
  if (spanMs > 3 * 86_400_000) return d.toLocaleDateString([], { weekday: 'short', day: 'numeric' })
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
}

/** Text-anchor for the k-th of `count` evenly spaced axis ticks: edges align outward, the rest centre. */
export function tickAnchor(k: number, count: number): 'start' | 'end' | 'middle' {
  if (k === 0) return 'start'
  if (k === count - 1) return 'end'
  return 'middle'
}
