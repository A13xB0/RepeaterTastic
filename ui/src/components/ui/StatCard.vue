<script setup lang="ts">
import Sparkline from '@/components/charts/Sparkline.vue'

withDefaults(defineProps<{ label: string; value: string | number; sub?: string; trend?: number[]; color?: string; tone?: 'ok' | 'warn' | 'bad' | '' }>(), {
  color: 'var(--brand)',
  tone: '',
})
</script>

<template>
  <div class="card flex min-w-0 flex-col justify-between gap-2 px-4 pb-3 pt-3.5">
    <div class="flex items-center justify-between gap-2">
      <span class="truncate text-xs font-medium text-ink-3">{{ label }}</span>
      <slot name="icon" />
    </div>
    <div class="flex items-end justify-between gap-3">
      <div class="min-w-0">
        <div class="truncate text-[22px] font-semibold leading-none tracking-tight tabular-nums">{{ value }}</div>
        <div
          v-if="sub"
          :class="['mt-1.5 truncate text-xs', tone === 'ok' ? 'text-ok' : tone === 'warn' ? 'text-warn' : tone === 'bad' ? 'text-bad' : 'text-ink-3']"
        >
          {{ sub }}
        </div>
      </div>
      <div v-if="trend && trend.length >= 3" class="w-[40%] max-w-24 shrink-0">
        <Sparkline :data="trend" :color="color" :height="30" />
      </div>
    </div>
  </div>
</template>
