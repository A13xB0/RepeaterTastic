<script setup lang="ts">
// Hand-rolled SVG line/area chart over time with one y-axis, optional reference line and hover crosshair.
import { computed, ref } from 'vue'
import { niceMax, tickAnchor, timeTick, useWidth } from './useWidth'

export interface TimeSeries {
  name: string
  color: string
  values: number[]
  area?: boolean
}

const props = withDefaults(
  defineProps<{
    times: number[]
    series: TimeSeries[]
    height?: number
    format?: (v: number) => string
    yMax?: number
    yMin?: number
    refLine?: { value: number; label: string; color?: string }
  }>(),
  { height: 220, format: (v: number) => v.toFixed(1) },
)

const el = ref<HTMLElement | null>(null)
const width = useWidth(el)
const pad = { l: 44, r: 12, t: 12, b: 24 }
const hover = ref<number | null>(null)

const scale = computed(() => {
  const all = props.series.flatMap((s) => s.values)
  const lo = props.yMin ?? Math.min(0, ...all)
  const top = Math.max(...all, props.refLine ? props.refLine.value * 1.15 : 0, lo + 1e-9)
  const hi = props.yMax ?? (lo < 0 ? top : niceMax(top))
  return { lo, hi }
})

const plotW = computed(() => Math.max(40, width.value - pad.l - pad.r))
const plotH = computed(() => props.height - pad.t - pad.b)
const n = computed(() => props.times.length)
const x = (i: number) => pad.l + (n.value <= 1 ? 0 : (i / (n.value - 1)) * plotW.value)
const y = (v: number) => pad.t + plotH.value - ((v - scale.value.lo) / (scale.value.hi - scale.value.lo || 1)) * plotH.value

const paths = computed(() =>
  props.series.map((s) => {
    const line = s.values.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('')
    const base = y(Math.max(scale.value.lo, 0))
    const area = s.area && s.values.length ? `${line}L${x(s.values.length - 1).toFixed(1)},${base}L${x(0).toFixed(1)},${base}Z` : ''
    return { ...s, line, area }
  }),
)

const yTicks = computed(() => {
  const { lo, hi } = scale.value
  return Array.from({ length: 5 }, (_, i) => lo + ((hi - lo) * i) / 4)
})

const xTicks = computed(() => {
  const count = Math.max(2, Math.min(7, Math.floor(plotW.value / 90)))
  if (n.value < 2) return []
  const span = props.times[n.value - 1]! - props.times[0]!
  return Array.from({ length: count }, (_, k) => {
    const i = Math.round((k / (count - 1)) * (n.value - 1))
    return { x: x(i), label: timeTick(props.times[i]!, span), anchor: tickAnchor(k, count) }
  })
})

function onMove(e: PointerEvent) {
  const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
  const px = e.clientX - rect.left
  if (n.value < 1) return
  hover.value = Math.max(0, Math.min(n.value - 1, Math.round(((px - pad.l) / plotW.value) * (n.value - 1))))
}

const tip = computed(() => {
  if (hover.value === null) return null
  const i = hover.value
  const t = props.times[i]!
  const left = x(i)
  return {
    left: Math.min(Math.max(left, 90), width.value - 90),
    time: new Date(t).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }),
    rows: props.series.map((s) => ({ name: s.name, color: s.color, value: props.format(s.values[i] ?? 0) })),
  }
})
</script>

<template>
  <div ref="el" class="relative w-full min-w-0 overflow-hidden select-none" :style="{ height: `${height}px` }">
    <svg :width="width" :height="height" class="block" @pointermove="onMove" @pointerleave="hover = null">
      <g>
        <line v-for="(t, i) in yTicks" :key="i" :x1="pad.l" :x2="width - pad.r" :y1="y(t)" :y2="y(t)" stroke="var(--grid)" stroke-width="1" />
        <text v-for="(t, i) in yTicks" :key="`l${i}`" :x="pad.l - 8" :y="y(t) + 3.5" text-anchor="end" class="fill-ink-3 text-[10.5px] tabular-nums">
          {{ format(t) }}
        </text>
        <text v-for="(t, i) in xTicks" :key="`x${i}`" :x="t.x" :y="height - 6" :text-anchor="t.anchor" class="fill-ink-3 text-[10.5px] tabular-nums">
          {{ t.label }}
        </text>
      </g>
      <g v-for="p in paths" :key="p.name">
        <path v-if="p.area" :d="p.area" :fill="p.color" fill-opacity="0.12" />
        <path :d="p.line" fill="none" :stroke="p.color" stroke-width="2" stroke-linejoin="round" stroke-linecap="round" />
      </g>
      <g v-if="refLine">
        <line :x1="pad.l" :x2="width - pad.r" :y1="y(refLine.value)" :y2="y(refLine.value)" :stroke="refLine.color ?? 'var(--bad)'" stroke-width="1.5" />
        <text :x="width - pad.r - 4" :y="y(refLine.value) - 5" text-anchor="end" stroke="var(--surface-solid)" stroke-width="4" paint-order="stroke" class="fill-ink-2 text-[10.5px] font-medium">{{ refLine.label }}</text>
      </g>
      <g v-if="hover !== null">
        <line :x1="x(hover)" :x2="x(hover)" :y1="pad.t" :y2="pad.t + plotH" stroke="var(--axis)" stroke-width="1" />
        <circle
          v-for="p in paths"
          :key="`c${p.name}`"
          :cx="x(hover)"
          :cy="y(p.values[hover] ?? 0)"
          r="4"
          :fill="p.color"
          stroke="var(--surface-solid)"
          stroke-width="2"
        />
      </g>
    </svg>
    <div
      v-if="tip"
      class="pointer-events-none absolute top-1 z-10 min-w-40 -translate-x-1/2 rounded-lg border border-line bg-surface-solid px-2.5 py-2 text-xs shadow-lg"
      :style="{ left: `${tip.left}px` }"
    >
      <div class="mb-1 font-medium text-ink-2">{{ tip.time }}</div>
      <div v-for="r in tip.rows" :key="r.name" class="flex items-center gap-2 py-px">
        <span class="h-0.5 w-3 rounded" :style="{ background: r.color }" />
        <span class="flex-1 text-ink-2">{{ r.name }}</span>
        <span class="font-semibold tabular-nums text-ink">{{ r.value }}</span>
      </div>
    </div>
  </div>
</template>
