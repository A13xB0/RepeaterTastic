<script setup lang="ts">
import { ref, watch } from 'vue'
import { Puzzle } from '@lucide/vue'
import type { Plugin } from '@/api/types'

const props = withDefaults(defineProps<{ plugin: Plugin; size?: 'md' | 'lg' }>(), { size: 'md' })
const failed = ref(false)
watch(() => props.plugin.logo_url, () => (failed.value = false))
</script>

<template>
  <span
    :class="[
      'flex shrink-0 items-center justify-center overflow-hidden rounded-xl border border-line-soft bg-raised text-ink-3',
      size === 'lg' ? 'size-14' : 'size-10',
    ]"
  >
    <img v-if="plugin.logo_url && !failed" :src="plugin.logo_url" alt="" class="size-full object-contain p-1" @error="failed = true" />
    <Puzzle v-else :class="size === 'lg' ? 'size-6' : 'size-5'" />
  </span>
</template>
