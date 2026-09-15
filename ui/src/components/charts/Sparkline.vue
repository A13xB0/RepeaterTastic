<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{ data: number[]; color?: string; height?: number; fill?: boolean; min?: number; max?: number }>(), {
  color: 'var(--brand)',
  height: 32,
  fill: true,
})

const W = 120
const path = computed(() => {
  const d = props.data
  if (d.length < 2) return { line: '', area: '', last: null as null | [number, number] }
  const lo = props.min ?? Math.min(...d)
  const hi = props.max ?? Math.max(...d)
  const span = hi - lo || 1
  const h = props.height
  const pts = d.map((v, i) => [(i / (d.length - 1)) * W, h - 3 - ((v - lo) / span) * (h - 6)] as [number, number])
  const line = pts.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)},${p[1].toFixed(1)}`).join('')
  return { line, area: `${line}L${W},${h}L0,${h}Z`, last: pts[pts.length - 1]! }
})
</script>

<template>
  <svg :viewBox="`0 0 ${W} ${height}`" preserveAspectRatio="none" class="block w-full overflow-visible" :style="{ height: `${height}px` }" aria-hidden="true">
    <path v-if="fill && path.area" :d="path.area" :fill="color" fill-opacity="0.1" />
    <path v-if="path.line" :d="path.line" fill="none" :stroke="color" stroke-width="1.75" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke" />
  </svg>
</template>
