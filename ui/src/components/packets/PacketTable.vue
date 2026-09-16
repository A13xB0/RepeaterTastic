<script setup lang="ts">
// Packet table modelled on openHop's PacketTable (MIT, © Lloyd Newton), rewritten for Meshtastic frames.
import { ArrowDownLeft, ArrowUpRight, Lock } from '@lucide/vue'
import type { Packet } from '@/api/types'
import KindChip from '@/components/ui/KindChip.vue'
import { nodeLabel, radioName } from '@/store/live'
import { BROADCAST, airtime, clock, portLabel, snrClass } from '@/lib/format'

withDefaults(defineProps<{ packets: Packet[]; compact?: boolean; flashSeq?: number; showRadio?: boolean }>(), { compact: false, flashSeq: 0, showRadio: false })
const emit = defineEmits<{ select: [p: Packet] }>()
</script>

<template>
  <div class="scroll-thin overflow-x-auto">
    <table class="tbl">
      <thead>
        <tr>
          <th>Time</th>
          <th>Kind</th>
          <th v-if="showRadio" class="max-md:hidden">Radio</th>
          <th>From → To</th>
          <th>Port</th>
          <th class="max-md:hidden">Channel</th>
          <th class="num">Hops</th>
          <th class="num">SNR</th>
          <th class="num max-sm:hidden">RSSI</th>
          <th v-if="!compact" class="num max-lg:hidden">Size</th>
          <th v-if="!compact" class="num max-lg:hidden">Airtime</th>
          <th class="max-xl:hidden">Summary</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="p in packets"
          :key="`${p.radio_id ?? 'main'}-${p.seq}`"
          :class="['row-link', p.seq > flashSeq && flashSeq > 0 ? 'flash-in' : '']"
          @click="emit('select', p)"
        >
          <td class="whitespace-nowrap tabular-nums text-ink-2">
            <span class="inline-flex items-center gap-1.5">
              <ArrowUpRight v-if="p.direction === 'tx'" class="size-3.5 text-brand" aria-label="transmitted" />
              <ArrowDownLeft v-else class="size-3.5 text-info" aria-label="received" />
              {{ clock(p.time) }}
            </span>
          </td>
          <td><KindChip :kind="p.kind" /></td>
          <td v-if="showRadio" class="whitespace-nowrap text-ink-2 max-md:hidden">{{ radioName(p.radio_id ?? 'main') }}</td>
          <td class="whitespace-nowrap">
            <span :class="['font-medium', nodeLabel(p.from).local ? 'text-brand' : '']" :title="`${nodeLabel(p.from).long} ${p.from}`">{{ nodeLabel(p.from).short }}</span>
            <span class="px-1 text-ink-3">→</span>
            <span :class="[p.to === BROADCAST ? 'text-ink-3' : nodeLabel(p.to).local ? 'font-medium text-brand' : 'font-medium']" :title="`${nodeLabel(p.to).long} ${p.to}`">
              {{ nodeLabel(p.to).short }}
            </span>
            <Lock v-if="p.pki" class="ml-1 inline size-3 text-ink-3" aria-label="PKI" />
          </td>
          <td class="whitespace-nowrap text-ink-2">{{ portLabel(p.port) }}</td>
          <td class="whitespace-nowrap text-ink-2 max-md:hidden">
            <template v-if="p.channel">{{ p.channel }}</template>
            <span v-else class="mono text-ink-3">#{{ p.channel_hash.toString(16).padStart(2, '0') }}</span>
          </td>
          <td class="num text-ink-2">{{ p.hop_start - p.hop_limit }}<span class="text-ink-3">/{{ p.hop_start }}</span></td>
          <td :class="['num', snrClass(p.snr)]">{{ p.snr == null ? '—' : p.snr.toFixed(1) }}</td>
          <td class="num text-ink-2 max-sm:hidden">{{ p.rssi ?? '—' }}</td>
          <td v-if="!compact" class="num text-ink-3 max-lg:hidden">{{ p.size }} B</td>
          <td v-if="!compact" class="num text-ink-3 max-lg:hidden">{{ airtime(p.airtime_ms) }}</td>
          <td class="max-w-[22rem] truncate text-ink-2 max-xl:hidden" :title="p.summary">{{ p.summary }}</td>
        </tr>
      </tbody>
    </table>
    <div v-if="!packets.length" class="empty">No packets yet.</div>
  </div>
</template>
