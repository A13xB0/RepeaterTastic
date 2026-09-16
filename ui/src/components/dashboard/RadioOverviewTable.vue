<script setup lang="ts">
// Compact per-radio breakdown for the site-wide ("all radios") dashboard: everything the single-radio
// dashboard shows about airtime and noise floor, one row per radio, since neither sums meaningfully
// across radios that can each run their own region, preset and duty-cycle limit.
import { computed } from 'vue'
import { live } from '@/store/live'
import { relayModeLabel } from '@/lib/relay'
import { num } from '@/lib/format'

function dutyLimit(id: string): number {
  return live.statuses[id]?.airtime.duty_limit_pct ?? 10
}
function gauge(id: string, txPct: number) {
  const ratio = txPct / (dutyLimit(id) || 100)
  return { pct: Math.min(100, ratio * 100), cls: ratio > 0.9 ? 'bg-bad' : ratio > 0.7 ? 'bg-warn' : 'bg-brand' }
}
const rows = computed(() => live.radios)
</script>

<template>
  <section class="card mb-4 overflow-hidden">
    <div class="card-head">
      <div>
        <h3 class="card-title">Radios</h3>
        <p class="card-sub">Airtime and noise floor are per radio, so they don't sum across the site</p>
      </div>
    </div>
    <div class="scroll-thin overflow-x-auto">
      <table class="tbl">
        <thead>
          <tr>
            <th>Radio</th>
            <th class="max-lg:hidden">Preset</th>
            <th>Status</th>
            <th class="max-md:hidden">Relay</th>
            <th class="min-w-36">Airtime TX</th>
            <th class="num max-xl:hidden">Ch. util</th>
            <th class="num">Noise floor</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.id">
            <td class="font-medium">{{ r.name }}</td>
            <td class="text-ink-2 max-lg:hidden">
              {{ r.phy.preset_name }}
              <div class="text-2xs tabular-nums text-ink-3">{{ r.phy.frequency_mhz.toFixed(3) }} MHz</div>
            </td>
            <td>
              <span :class="['chip', r.connected ? 'bg-ok/14 text-ok' : 'bg-warn/15 text-warn']">
                <span :class="['dot size-1.5', r.connected ? 'bg-ok' : 'bg-warn']" />{{ r.connected ? 'on air' : 'offline' }}
              </span>
            </td>
            <td class="text-ink-2 max-md:hidden">{{ relayModeLabel(r.relay.role) }}</td>
            <td>
              <div class="flex items-baseline justify-between gap-2 text-xs">
                <span class="font-medium tabular-nums">{{ r.tx_pct.toFixed(1) }}%</span>
                <span class="tabular-nums text-ink-3">/ {{ num(dutyLimit(r.id)) }}%</span>
              </div>
              <div class="mt-1 h-1.5 rounded-full bg-ink-3/15">
                <div :class="['h-full rounded-full', gauge(r.id, r.tx_pct).cls]" :style="{ width: `${Math.max(gauge(r.id, r.tx_pct).pct, 1.5)}%` }" />
              </div>
            </td>
            <td class="num tabular-nums max-xl:hidden">{{ r.channel_util_pct.toFixed(1) }}%</td>
            <td class="num tabular-nums">{{ num(r.noise_floor_dbm, 0) }} dBm</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!rows.length" class="empty">Loading radios…</div>
    </div>
  </section>
</template>
