<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { AirtimeStats, IdentityStat, PortStat, RfStats, StatsWindow } from '@/api/types'
import { live, nodeLabel } from '@/store/live'
import StackedBars from '@/components/charts/StackedBars.vue'
import TimeChart from '@/components/charts/TimeChart.vue'
import HBars from '@/components/charts/HBars.vue'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import { toastError } from '@/composables/toast'
import { compact, portLabel, seconds } from '@/lib/format'

const win = ref<StatsWindow>('24h')
const airtime = ref<AirtimeStats | null>(null)
const rf = ref<RfStats | null>(null)
const ports = ref<PortStat[]>([])
const idents = ref<IdentityStat[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const w = win.value
    const [a, r, p, i] = await Promise.all([
      api.get<AirtimeStats>(`/stats/airtime?window=${w}`),
      api.get<RfStats>(`/stats/rf?window=${w}`),
      api.get<PortStat[]>(`/stats/ports?window=${w}`),
      api.get<IdentityStat[]>(`/stats/identities?window=${w}`),
    ])
    airtime.value = a
    rf.value = r
    ports.value = p
    idents.value = i
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}
watch(win, load, { immediate: true })

const SLOTS = ['var(--s1)', 'var(--s2)', 'var(--s3)', 'var(--s4)', 'var(--s5)', 'var(--s6)', 'var(--s7)']
// Colour follows the identity (sorted by id), not its rank, so filters never repaint series.
const identityColors = computed(() => {
  const ids = [...new Set(airtime.value?.buckets.flatMap((b) => Object.keys(b.by_identity)) ?? [])].sort()
  return new Map(ids.map((id, i) => [id, SLOTS[(i + 1) % SLOTS.length]!]))
})

const unit = computed(() => {
  const bucket = airtime.value?.bucket_s ?? 60
  return bucket >= 3600 ? { div: 1000, fmt: (v: number) => `${compact(Math.round(v))} s` } : { div: 1000, fmt: (v: number) => `${v.toFixed(v < 10 ? 1 : 0)} s` }
})

const stacked = computed(() => {
  const a = airtime.value
  if (!a) return null
  const ids = [...identityColors.value.keys()]
  return {
    times: a.buckets.map((b) => b.time),
    series: [
      { name: 'Relay', color: 'var(--ink-3)', values: a.buckets.map((b) => b.relay_ms / unit.value.div) },
      ...ids.map((id) => ({ name: nodeLabel(id).long, color: identityColors.value.get(id)!, values: a.buckets.map((b) => (b.by_identity[id] ?? 0) / unit.value.div) })),
    ],
    budget: ((live.status?.airtime.duty_limit_pct ?? 10) / 100) * a.bucket_s,
  }
})

const totals = computed(() => {
  const a = airtime.value
  if (!a) return null
  const span = a.buckets.length * a.bucket_s * 1000
  const tx = a.buckets.reduce((s, b) => s + b.tx_ms, 0)
  const relay = a.buckets.reduce((s, b) => s + b.relay_ms, 0)
  const rx = a.buckets.reduce((s, b) => s + b.rx_ms, 0)
  const util = rf.value?.points.length ? rf.value.points.reduce((s, p) => s + p.channel_util_pct, 0) / rf.value.points.length : 0
  return { tx, relay, rx, txPct: (tx / span) * 100, util }
})

const rfCharts = computed(() => {
  const r = rf.value
  if (!r) return null
  return {
    times: r.points.map((p) => p.time),
    noise: [{ name: 'Noise floor', color: 'var(--s1)', values: r.points.map((p) => p.noise_floor_dbm) }],
    util: [{ name: 'Channel utilisation', color: 'var(--s3)', values: r.points.map((p) => p.channel_util_pct), area: true }],
  }
})

const identityRows = computed(() =>
  idents.value
    .map((s) => ({ ...s, ident: live.identities.find((i) => i.node_id === s.node_id), ack: s.ack_ok + s.ack_fail ? (s.ack_ok / (s.ack_ok + s.ack_fail)) * 100 : null }))
    .sort((a, b) => b.airtime_ms - a.airtime_ms),
)
const maxAir = computed(() => Math.max(1, ...idents.value.map((i) => i.airtime_ms)))

const portRows = computed(() => [...ports.value].sort((a, b) => b.rx + b.tx - (a.rx + a.tx)).map((p) => ({ label: portLabel(p.port), values: [p.rx, p.tx] })))
const windows: { id: StatsWindow; label: string }[] = [
  { id: '1h', label: '1 h' },
  { id: '24h', label: '24 h' },
  { id: '7d', label: '7 d' },
]
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Statistics</h2>
        <p class="page-sub">Who is using the shared airtime, and how the channel is doing</p>
      </div>
      <div class="seg" role="group" aria-label="Window">
        <button v-for="w in windows" :key="w.id" :aria-pressed="win === w.id" :disabled="loading" @click="win = w.id">{{ w.label }}</button>
      </div>
    </div>

    <div class="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
      <div v-for="c in [
        { label: 'TX airtime', value: totals ? seconds(totals.tx) : '—', sub: totals ? `${totals.txPct.toFixed(2)}% of the window` : '' },
        { label: 'Relayed for others', value: totals ? seconds(totals.relay) : '—', sub: totals && totals.tx ? `${((totals.relay / totals.tx) * 100).toFixed(0)}% of our TX` : '' },
        { label: 'RX airtime', value: totals ? seconds(totals.rx) : '—', sub: 'time spent receiving' },
        { label: 'Avg channel util', value: totals ? `${totals.util.toFixed(1)}%` : '—', sub: 'as reported by the modem' },
      ]" :key="c.label" class="card px-4 py-3.5">
        <div class="text-xs font-medium text-ink-3">{{ c.label }}</div>
        <div class="mt-1.5 text-[22px] font-semibold leading-none tracking-tight tabular-nums">{{ c.value }}</div>
        <div class="mt-1.5 text-xs text-ink-3">{{ c.sub }}</div>
      </div>
    </div>

    <section class="card mb-4">
      <div class="card-head">
        <div>
          <h3 class="card-title">Airtime per identity</h3>
          <p class="card-sub">Seconds of TX per {{ airtime ? (airtime.bucket_s >= 3600 ? 'hour' : `${airtime.bucket_s / 60} min`) : 'bucket' }}, stacked; the red line is the duty-cycle budget for one bucket</p>
        </div>
        <div v-if="stacked" class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-2">
          <span v-for="s in stacked.series" :key="s.name" class="inline-flex items-center gap-1.5"><span class="size-2 rounded-sm" :style="{ background: s.color }" />{{ s.name }}</span>
        </div>
      </div>
      <div class="px-2 pb-3 sm:px-3">
        <StackedBars
          v-if="stacked"
          :times="stacked.times"
          :series="stacked.series"
          :height="260"
          :format="unit.fmt"
          :ref-line="{ value: stacked.budget, label: `${live.status?.airtime.duty_limit_pct ?? 10}% budget` }"
        />
        <div v-else class="mx-3 h-[260px] animate-pulse rounded-xl bg-sunken" />
      </div>
    </section>

    <div class="mb-4 grid gap-4 xl:grid-cols-2">
      <section class="card overflow-hidden">
        <div class="card-head">
          <h3 class="card-title">Identities</h3>
          <span class="text-2xs text-ink-3">airtime · packets · ACK success</span>
        </div>
        <div class="scroll-thin overflow-x-auto">
          <table class="tbl">
            <thead>
              <tr><th>Identity</th><th>Airtime</th><th class="num">TX</th><th class="num">RX</th><th>ACK success</th></tr>
            </thead>
            <tbody>
              <tr v-for="r in identityRows" :key="r.node_id">
                <td>
                  <div class="flex items-center gap-2">
                    <NodeAvatar :id="r.node_id" :short="r.ident?.short_name ?? r.node_id.slice(-4)" size="sm" />
                    <span class="truncate font-medium">{{ r.ident?.long_name ?? r.node_id }}</span>
                  </div>
                </td>
                <td class="min-w-36">
                  <div class="flex items-center gap-2">
                    <div class="h-1.5 flex-1 rounded-full bg-ink-3/15">
                      <div class="h-full rounded-full" :style="{ width: `${(r.airtime_ms / maxAir) * 100}%`, background: r.ident?.is_relay ? 'var(--ink-3)' : (identityColors.get(r.node_id) ?? 'var(--s1)') }" />
                    </div>
                    <span class="w-14 text-right text-xs tabular-nums">{{ seconds(r.airtime_ms) }}</span>
                  </div>
                </td>
                <td class="num">{{ compact(r.tx) }}</td>
                <td class="num text-ink-2">{{ compact(r.rx) }}</td>
                <td class="whitespace-nowrap">
                  <template v-if="r.ack !== null">
                    <span :class="['font-medium tabular-nums', r.ack < 85 ? 'text-warn' : '']">{{ r.ack.toFixed(0) }}%</span>
                    <span class="text-xs text-ink-3"> · {{ r.ack_fail }} failed</span>
                  </template>
                  <span v-else class="text-ink-3">—</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <div class="card-head">
          <h3 class="card-title">Packets by port</h3>
          <span class="text-2xs text-ink-3">hover a row for RX / TX</span>
        </div>
        <div class="px-4 pb-4 sm:px-5">
          <HBars :rows="portRows" :series="[{ name: 'RX', color: 'var(--s1)' }, { name: 'TX', color: 'var(--s3)' }]" :format="compact" />
        </div>
      </section>
    </div>

    <div class="grid gap-4 xl:grid-cols-2">
      <section class="card">
        <div class="card-head"><h3 class="card-title">Noise floor</h3><span class="text-2xs text-ink-3">dBm</span></div>
        <div class="px-2 pb-3 sm:px-3">
          <TimeChart v-if="rfCharts" :times="rfCharts.times" :series="rfCharts.noise" :height="180" :format="(v: number) => v.toFixed(0)" :y-min="-126" :y-max="-104" />
        </div>
      </section>
      <section class="card">
        <div class="card-head"><h3 class="card-title">Channel utilisation</h3><span class="text-2xs text-ink-3">% of time the channel was busy</span></div>
        <div class="px-2 pb-3 sm:px-3">
          <TimeChart v-if="rfCharts" :times="rfCharts.times" :series="rfCharts.util" :height="180" :format="(v: number) => `${v.toFixed(0)}%`" />
        </div>
      </section>
    </div>
  </div>
</template>
