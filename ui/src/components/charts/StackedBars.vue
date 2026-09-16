<script setup lang="ts">
// Stacked columns over time with a 2px surface gap between segments and per-column hover tooltip.
import { computed, ref } from 'vue'
import { niceMax, tickAnchor, timeTick, useWidth } from './useWidth'

export interface BarSeries {
  name: string
  color: string
  values: number[]
}

const props = withDefaults(
  defineProps<{ times: number[]; series: BarSeries[]; height?: number; format?: (v: number) => string; refLine?: { value: number; label: string } }>(),
  { height: 240, format: (v: number) => String(Math.round(v)) },
)

const el = ref<HTMLElement | null>(null)
const width = useWidth(el)
const pad = { l: 48, r: 12, t: 12, b: 24 }
const hover = ref<number | null>(null)

const n = computed(() => props.times.length)
const totals = computed(() => props.times.map((_, i) => props.series.reduce((s, x) => s + (x.values[i] ?? 0), 0)))
const hi = computed(() => niceMax(Math.max(...totals.value, props.refLine ? props.refLine.value * 1.1 : 0, 1)))
const plotW = computed(() => Math.max(40, width.value - pad.l - pad.r))
const plotH = computed(() => props.height - pad.t - pad.b)
const band = computed(() => plotW.value / Math.max(1, n.value))
const barW = computed(() => Math.max(1, Math.min(24, band.value - 2)))
const y = (v: number) => pad.t + plotH.value - (v / hi.value) * plotH.value

const columns = computed(() =>
  props.times.map((_, i) => {
    let acc = 0
    const cx = pad.l + band.value * i + (band.value - barW.value) / 2
    const segs = props.series
      .map((s) => {
        const v = s.values[i] ?? 0
        const y0 = y(acc)
        acc += v
        const y1 = y(acc)
        return { color: s.color, x: cx, y: y1, h: Math.max(0, y0 - y1), v }
      })
      .filter((s) => s.v > 0)
    // 2px surface gap between stacked segments
    for (let k = 1; k < segs.length; k++) segs[k]!.h = Math.max(0, segs[k]!.h - (barW.value > 3 ? 2 : 0))
    return segs
  }),
)

function topPath(s: { x: number; y: number; h: number }) {
  const r = Math.min(barW.value > 6 ? 4 : 1, s.h, barW.value / 2)
  const w = barW.value
  return `M${s.x},${s.y + s.h}V${s.y + r}Q${s.x},${s.y} ${s.x + r},${s.y}H${s.x + w - r}Q${s.x + w},${s.y} ${s.x + w},${s.y + r}V${s.y + s.h}Z`
}

const yTicks = computed(() => Array.from({ length: 5 }, (_, i) => (hi.value * i) / 4))
const xTicks = computed(() => {
  if (n.value < 2) return []
  const count = Math.max(2, Math.min(7, Math.floor(plotW.value / 90)))
  const span = props.times[n.value - 1]! - props.times[0]!
  return Array.from({ length: count }, (_, k) => {
    const i = Math.round((k / (count - 1)) * (n.value - 1))
    return { x: pad.l + band.value * i + band.value / 2, label: timeTick(props.times[i]!, span), anchor: tickAnchor(k, count) }
  })
})

function onMove(e: PointerEvent) {
  const rect = (e.currentTarget as SVGElement).getBoundingClientRect()
  const i = Math.floor((e.clientX - rect.left - pad.l) / band.value)
  hover.value = i >= 0 && i < n.value ? i : null
}

const tip = computed(() => {
  if (hover.value === null) return null
  const i = hover.value
  return {
    left: Math.min(Math.max(pad.l + band.value * (i + 0.5), 100), width.value - 100),
    time: new Date(props.times[i]!).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }),
    rows: props.series.map((s) => ({ name: s.name, color: s.color, value: props.format(s.values[i] ?? 0) })).reverse(),
    total: props.format(totals.value[i] ?? 0),
  }
})
</script>

<template>
  <div ref="el" class="relative w-full min-w-0 overflow-hidden select-none" :style="{ height: `${height}px` }">
    <svg :width="width" :height="height" class="block" @pointermove="onMove" @pointerleave="hover = null">
      <line v-for="(t, i) in yTicks" :key="i" :x1="pad.l" :x2="width - pad.r" :y1="y(t)" :y2="y(t)" stroke="var(--grid)" stroke-width="1" />
      <text v-for="(t, i) in yTicks" :key="`l${i}`" :x="pad.l - 8" :y="y(t) + 3.5" text-anchor="end" class="fill-ink-3 text-[10.5px] tabular-nums">
        {{ format(t) }}
      </text>
      <text v-for="(t, i) in xTicks" :key="`x${i}`" :x="t.x" :y="height - 6" :text-anchor="t.anchor" class="fill-ink-3 text-[10.5px] tabular-nums">
        {{ t.label }}
      </text>
      <rect
        v-if="hover !== null"
        :x="pad.l + band * hover"
        :y="pad.t"
        :width="band"
        :height="plotH"
        fill="var(--ink)"
        fill-opacity="0.04"
      />
      <g v-for="(col, i) in columns" :key="i">
        <template v-for="(s, k) in col" :key="k">
          <path v-if="k === col.length - 1" :d="topPath(s)" :fill="s.color" />
          <rect v-else :x="s.x" :y="s.y" :width="barW" :height="s.h" :fill="s.color" />
        </template>
      </g>
      <g v-if="refLine">
        <line :x1="pad.l" :x2="width - pad.r" :y1="y(refLine.value)" :y2="y(refLine.value)" stroke="var(--bad)" stroke-width="1.5" />
        <text :x="width - pad.r - 4" :y="y(refLine.value) - 5" text-anchor="end" stroke="var(--surface-solid)" stroke-width="4" paint-order="stroke" class="fill-ink-2 text-[10.5px] font-medium">{{ refLine.label }}</text>
      </g>
    </svg>
    <div
      v-if="tip"
      class="pointer-events-none absolute top-1 z-10 min-w-44 -translate-x-1/2 rounded-lg border border-line bg-surface-solid px-2.5 py-2 text-xs shadow-lg"
      :style="{ left: `${tip.left}px` }"
    >
      <div class="mb-1 flex justify-between gap-3 font-medium text-ink-2">
        <span>{{ tip.time }}</span><span class="tabular-nums text-ink">{{ tip.total }}</span>
      </div>
      <div v-for="r in tip.rows" :key="r.name" class="flex items-center gap-2 py-px">
        <span class="size-2 rounded-sm" :style="{ background: r.color }" />
        <span class="flex-1 truncate text-ink-2">{{ r.name }}</span>
        <span class="font-semibold tabular-nums text-ink">{{ r.value }}</span>
      </div>
    </div>
  </div>
</template>
