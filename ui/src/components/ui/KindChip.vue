<script setup lang="ts">
import type { PacketKind } from '@/api/types'

defineProps<{ kind: PacketKind }>()

const styles: Record<PacketKind, string> = {
  ours: 'bg-brand/14 text-brand',
  delivered: 'bg-info/12 text-info',
  relayed: 'bg-warn/14 text-warn',
  dup: 'bg-ink-3/14 text-ink-3',
  undecryptable: 'bg-bad/12 text-bad',
  local: 'bg-violet/14 text-violet',
}
const labels: Record<PacketKind, string> = {
  ours: 'ours', delivered: 'delivered', relayed: 'relayed', dup: 'dup', undecryptable: 'undecryptable', local: 'local',
}
</script>

<template>
  <span :class="['chip', styles[kind]]">{{ labels[kind] }}</span>
</template>
