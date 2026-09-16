<script setup lang="ts">
// How the meshtasticd nodes are doing: green when all run, amber when some identities are down,
// red when meshtasticd isn't running at all.
import { computed } from 'vue'
import { CircleCheck, CircleX, LoaderCircle, TriangleAlert } from '@lucide/vue'
import type { NodesHealth } from '@/api/types'

const props = defineProps<{ health: NodesHealth; compact?: boolean }>()

const look = computed(() => {
  const h = props.health
  const down = h.nodes - h.up
  switch (h.state) {
    case 'error':
      return { icon: CircleX, cls: 'bg-bad/12 text-bad', text: h.up === 0 ? 'meshtasticd not running' : 'relay down' }
    case 'warning':
      return { icon: TriangleAlert, cls: 'bg-warn/15 text-warn', text: `${down} of ${h.nodes} down` }
    case 'starting':
      return { icon: LoaderCircle, cls: 'bg-info/12 text-info', text: 'starting' }
    default:
      return { icon: CircleCheck, cls: 'bg-ok/14 text-ok', text: 'OK' }
  }
})
const title = computed(() => {
  const h = props.health
  const version = h.version ? ` ${h.version}` : ''
  const head = `meshtasticd${version} · ${h.up}/${h.nodes} nodes running`
  return [head, ...h.problems].join('\n')
})
</script>

<template>
  <span :class="['chip gap-1', look.cls]" :title="title">
    <component :is="look.icon" :class="['size-3.5 shrink-0', health.state === 'starting' && 'animate-spin motion-reduce:animate-none']" aria-hidden="true" />
    <span v-if="compact" class="sr-only">meshtasticd: </span>
    <span>{{ look.text }}</span>
  </span>
</template>
