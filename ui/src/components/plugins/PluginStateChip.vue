<script setup lang="ts">
import { computed } from 'vue'
import type { Plugin } from '@/api/types'
import { stateLabel, stateTone, toneChip, toneDot } from './PluginBits'

const props = defineProps<{ plugin: Plugin }>()
const tone = computed(() => stateTone(props.plugin))
</script>

<template>
  <span :class="['chip', toneChip[tone]]" :title="plugin.detail">
    <span :class="['dot size-1.5', toneDot[tone], plugin.state === 'starting' || plugin.state === 'restarting' ? 'animate-pulse' : '']" />
    {{ stateLabel[plugin.state] ?? plugin.state }}
  </span>
</template>
