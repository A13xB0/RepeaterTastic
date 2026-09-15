<script setup lang="ts">
import { computed } from 'vue'
import { encode } from 'uqr'

const props = withDefaults(defineProps<{ value: string; size?: number }>(), { size: 220 })

const qr = computed(() => {
  if (!props.value) return null
  const { data, size } = encode(props.value, { ecc: 'M', border: 2 })
  let d = ''
  for (let y = 0; y < size; y++) {
    let x = 0
    while (x < size) {
      if (!data[y]![x]) {
        x++
        continue
      }
      const start = x
      while (x < size && data[y]![x]) x++
      d += `M${start},${y}h${x - start}v1h${start - x}z`
    }
  }
  return { d, size }
})
</script>

<template>
  <svg v-if="qr" :viewBox="`0 0 ${qr.size} ${qr.size}`" :width="size" :height="size" shape-rendering="crispEdges" class="rounded-xl bg-white" role="img" aria-label="QR code">
    <path :d="qr.d" fill="#0d1a17" />
  </svg>
</template>
