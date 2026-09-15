<script setup lang="ts">
// Horizontal stacked bars (e.g. packets by port: RX + TX), value at the bar end, hover shows the split.
import { computed, ref } from 'vue'

const props = defineProps<{
  rows: { label: string; values: number[] }[]
  series: { name: string; color: string }[]
  format?: (v: number) => string
}>()

const fmt = (v: number) => (props.format ? props.format(v) : v.toLocaleString())
const hover = ref<number | null>(null)
const max = computed(() => Math.max(1, ...props.rows.map((r) => r.values.reduce((a, b) => a + b, 0))))
</script>

<template>
  <div class="space-y-2.5">
    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-2">
      <span v-for="s in series" :key="s.name" class="inline-flex items-center gap-1.5">
        <span class="size-2 rounded-sm" :style="{ background: s.color }" />{{ s.name }}
      </span>
    </div>
    <div
      v-for="(r, i) in rows"
      :key="r.label"
      class="grid grid-cols-[7.5rem_1fr] items-center gap-3 text-xs sm:grid-cols-[9rem_1fr]"
      @pointerenter="hover = i"
      @pointerleave="hover = null"
    >
      <span class="truncate text-ink-2" :title="r.label">{{ r.label }}</span>
      <div class="flex min-w-0 items-center gap-2">
        <div class="flex h-3.5 min-w-0 gap-[2px]" :style="{ width: `${(r.values.reduce((a, b) => a + b, 0) / max) * 78}%` }">
          <span
            v-for="(v, k) in r.values"
            v-show="v > 0"
            :key="k"
            :class="['h-full', k === r.values.length - 1 || r.values.slice(k + 1).every((x) => x === 0) ? 'rounded-r-[4px]' : '']"
            :style="{ flexGrow: v, background: series[k]?.color }"
          />
        </div>
        <span class="shrink-0 tabular-nums text-ink">
          <template v-if="hover === i">
            <template v-for="(v, k) in r.values" :key="k"><span v-if="k" class="text-ink-3"> / </span>{{ fmt(v) }}</template>
          </template>
          <template v-else>{{ fmt(r.values.reduce((a, b) => a + b, 0)) }}</template>
        </span>
      </div>
    </div>
  </div>
</template>
