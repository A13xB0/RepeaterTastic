<script setup lang="ts">
import { computed } from 'vue'
import { nodeHue } from '@/lib/format'

const props = withDefaults(defineProps<{ id: string; short: string; size?: 'sm' | 'md' | 'lg'; local?: boolean }>(), { size: 'md' })
const hue = computed(() => nodeHue(props.id))
const sizes = { sm: 'h-6 min-w-9 text-[10px] rounded-md', md: 'h-8 min-w-11 text-[11px] rounded-lg', lg: 'h-11 min-w-14 text-[13px] rounded-xl' }
</script>

<template>
  <span
    :class="['relative inline-flex shrink-0 items-center justify-center px-1.5 font-mono font-bold tracking-tight', sizes[size]]"
    :style="{
      background: `color-mix(in oklab, hsl(${hue} 70% 50%) 16%, transparent)`,
      color: `color-mix(in oklab, hsl(${hue} 65% 42%) 100%, var(--ink) 15%)`,
    }"
  >
    {{ short || '?' }}
    <span v-if="local" class="absolute -right-1 -top-1 size-2.5 rounded-full border-2 border-surface-solid bg-brand" title="Local identity" />
  </span>
</template>
