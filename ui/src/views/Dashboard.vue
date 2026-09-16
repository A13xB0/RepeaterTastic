<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Pause, Play } from '@lucide/vue'
import { api } from '@/api/client'
import type { AirtimeStats, Packet, PortStat, RfStats } from '@/api/types'
import { live, packets } from '@/store/live'
import StatCard from '@/components/ui/StatCard.vue'
import TimeChart from '@/components/charts/TimeChart.vue'
import Sparkline from '@/components/charts/Sparkline.vue'
import HBars from '@/components/charts/HBars.vue'
import PacketTable from '@/components/packets/PacketTable.vue'
import PacketDrawer from '@/components/packets/PacketDrawer.vue'
import { compact, portLabel, relTime } from '@/lib/format'
import { now } from '@/composables/now'

const s = computed(() => live.status)
const airtime = ref<AirtimeStats | null>(null)
const rf = ref<RfStats | null>(null)
const ports = ref<PortStat[]>([])
const selected = ref<Packet | null>(null)
const paused = ref(false)
const frozen = ref<Packet[]>([])
const flashSeq = ref(0)

async function load() {
  const [a, r, p] = await Promise.allSettled([
    api.get<AirtimeStats>('/stats/airtime?window=1h'),
    api.get<RfStats>('/stats/rf?window=1h'),
    api.get<PortStat[]>('/stats/ports?window=24h'),
  ])
  if (a.status === 'fulfilled') airtime.value = a.value
  if (r.status === 'fulfilled') rf.value = r.value
  if (p.status === 'fulfilled') ports.value = p.value
}
let timer: number | undefined
onMounted(() => {
  load()
  timer = window.setInterval(load, 60_000)
})
onBeforeUnmount(() => clearInterval(timer))

watch(packets, (_list, old) => {
  flashSeq.value = old?.length ? (old[0]?.seq ?? 0) : 0
})
const visiblePackets = computed(() => (paused.value ? frozen.value : packets.value).slice(0, 40))
function togglePause() {
  paused.value = !paused.value
  if (paused.value) frozen.value = packets.value
}

// per-5s deltas from status history for the stat-card trends
const deltas = (key: 'rx' | 'tx' | 'dupe' | 'relayed' | 'undecryptable') =>
  computed(() => live.history.slice(1).map((h, i) => h[key] - live.history[i]![key]))
const rxTrend = deltas('rx')
const txTrend = deltas('tx')
const dupeTrend = deltas('dupe')
const relayTrend = deltas('relayed')
const undecTrend = deltas('undecryptable')

const ackPct = computed(() => {
  const c = s.value?.counters
  if (!c || c.ack_ok + c.ack_fail === 0) return null
  return (c.ack_ok / (c.ack_ok + c.ack_fail)) * 100
})
const nodesActive = computed(() => Object.values(live.nodes).filter((n) => !n.local && now.value - n.last_heard < 2 * 3600_000).length)
const nodesTotal = computed(() => Object.values(live.nodes).filter((n) => !n.local).length)

const airChart = computed(() => {
  const a = airtime.value
  if (!a) return null
  const ms = a.bucket_s * 1000
  return {
    times: a.buckets.map((b) => b.time),
    series: [
      { name: 'Channel busy (RX)', color: 'var(--s1)', values: a.buckets.map((b) => (b.rx_ms / ms) * 100) },
      { name: 'Our TX', color: 'var(--s3)', values: a.buckets.map((b) => (b.tx_ms / ms) * 100), area: true },
    ],
  }
})

const noiseSeries = computed(() => {
  const hist = rf.value?.points.map((p) => p.noise_floor_dbm) ?? []
  return [...hist.slice(-40), ...live.history.map((h) => h.noise)]
})

const portRows = computed(() =>
  [...ports.value].sort((a, b) => b.rx + b.tx - (a.rx + a.tx)).slice(0, 7).map((p) => ({ label: portLabel(p.port), values: [p.rx, p.tx] })),
)

const lastPacket = computed(() => packets.value[0])
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Dashboard</h2>
        <p class="page-sub">
          {{ live.identities.length }} identities on one modem · {{ nodesActive }} of {{ nodesTotal }} nodes heard in 2 h · last packet
          {{ lastPacket ? relTime(lastPacket.time, now) : '—' }}
        </p>
      </div>
    </div>

    <div class="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
      <StatCard label="Received" :value="s ? compact(s.counters.rx) : '—'" :sub="s ? `${s.radio.errors} CRC errors` : ''" :trend="rxTrend" color="var(--s1)" />
      <StatCard label="Transmitted" :value="s ? compact(s.counters.tx) : '—'" :sub="s ? `${s.counters.dropped_duty} duty drops` : ''" :trend="txTrend" color="var(--s3)" />
      <StatCard
        label="Duplicates"
        :value="s ? compact(s.counters.rx_dupe) : '—'"
        :sub="s && s.counters.rx ? `${((s.counters.rx_dupe / s.counters.rx) * 100).toFixed(0)}% of RX` : ''"
        :trend="dupeTrend"
        color="var(--ink-3)"
      />
      <StatCard label="Relayed" :value="s ? compact(s.counters.relayed) : '—'" :sub="s ? `${s.counters.relay_cancelled} cancelled` : ''" :trend="relayTrend" color="var(--s4)" />
      <StatCard
        label="ACK success"
        :value="ackPct === null ? '—' : `${ackPct.toFixed(0)}%`"
        :sub="s ? `${s.counters.ack_ok} ok · ${s.counters.ack_fail} failed` : ''"
        :tone="ackPct !== null && ackPct < 80 ? 'warn' : ''"
      />
      <StatCard
        label="Undecryptable"
        :value="s ? compact(s.counters.rx_undecryptable) : '—'"
        :sub="s && s.counters.rx ? `${((s.counters.rx_undecryptable / s.counters.rx) * 100).toFixed(0)}% of RX` : ''"
        :trend="undecTrend"
        color="var(--s8)"
      />
    </div>

    <div class="mb-4 grid gap-4 xl:grid-cols-3">
      <section class="card xl:col-span-2">
        <div class="card-head">
          <div>
            <h3 class="card-title">Airtime · last hour</h3>
            <p class="card-sub">Share of each minute spent transmitting vs. hearing traffic, against the region duty cycle</p>
          </div>
          <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-2">
            <span class="inline-flex items-center gap-1.5"><span class="h-0.5 w-3 rounded bg-s3" />Our TX</span>
            <span class="inline-flex items-center gap-1.5"><span class="h-0.5 w-3 rounded bg-s1" />Channel busy</span>
            <span class="inline-flex items-center gap-1.5"><span class="h-0.5 w-3 rounded bg-bad" />Duty limit</span>
          </div>
        </div>
        <div class="px-2 pb-3 sm:px-3">
          <TimeChart
            v-if="airChart"
            :times="airChart.times"
            :series="airChart.series"
            :height="240"
            :format="(v: number) => `${v.toFixed(0)}%`"
            :ref-line="s ? { value: s.airtime.duty_limit_pct, label: `${s.airtime.duty_limit_pct}% duty cycle` } : undefined"
          />
          <div v-else class="mx-3 h-[240px] animate-pulse rounded-xl bg-sunken" />
        </div>
      </section>

      <div class="grid gap-4">
        <section class="card">
          <div class="card-head">
            <h3 class="card-title">Noise floor</h3>
            <span class="text-2xs text-ink-3">last hour + live</span>
          </div>
          <div class="px-4 pb-4 sm:px-5">
            <div class="flex items-end gap-4">
              <div>
                <div class="text-[26px] font-semibold leading-none tracking-tight tabular-nums">{{ s?.radio.noise_floor_dbm ?? '—' }}<span class="ml-1 text-sm font-normal text-ink-3">dBm</span></div>
                <div class="mt-1.5 text-xs text-ink-3">channel util {{ s?.airtime.channel_util_pct.toFixed(1) ?? '—' }}%</div>
              </div>
              <Sparkline class="flex-1" :data="noiseSeries" :height="52" color="var(--info)" />
            </div>
          </div>
        </section>
        <section class="card">
          <div class="card-head">
            <h3 class="card-title">Packets by port</h3>
            <span class="text-2xs text-ink-3">24 h</span>
          </div>
          <div class="px-4 pb-4 sm:px-5">
            <HBars
              v-if="portRows.length"
              :rows="portRows"
              :series="[{ name: 'RX', color: 'var(--s1)' }, { name: 'TX', color: 'var(--s3)' }]"
            />
            <div v-else class="h-40 animate-pulse rounded-xl bg-sunken" />
          </div>
        </section>
      </div>
    </div>

    <section class="card overflow-hidden">
      <div class="card-head">
        <div>
          <h3 class="card-title">Live packets</h3>
          <p class="card-sub">Newest first · click a row for the header byte map and decoded payload</p>
        </div>
        <div class="flex items-center gap-2">
          <span v-if="paused" class="text-xs text-warn">Paused</span>
          <button type="button" class="btn btn-sm" @click="togglePause">
            <Play v-if="paused" class="size-3.5" /><Pause v-else class="size-3.5" />{{ paused ? 'Resume' : 'Pause' }}
          </button>
          <RouterLink to="/packets" class="btn btn-sm btn-ghost">Archive</RouterLink>
        </div>
      </div>
      <div class="max-h-[560px] overflow-y-auto">
        <PacketTable :packets="visiblePackets" compact :flash-seq="paused ? 0 : flashSeq" @select="selected = $event" />
      </div>
    </section>

    <PacketDrawer :packet="selected" @close="selected = null" />
  </div>
</template>
