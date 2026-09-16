<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Pause, Play } from '@lucide/vue'
import { MAIN_RADIO, api, withRadio } from '@/api/client'
import type { AirtimeStats, Packet, PortStat, RfStats, Status } from '@/api/types'
import { live, packets, radioName } from '@/store/live'
import StatCard from '@/components/ui/StatCard.vue'
import RadioFilter from '@/components/ui/RadioFilter.vue'
import RadioOverviewTable from '@/components/dashboard/RadioOverviewTable.vue'
import TimeChart from '@/components/charts/TimeChart.vue'
import Sparkline from '@/components/charts/Sparkline.vue'
import HBars from '@/components/charts/HBars.vue'
import PacketTable from '@/components/packets/PacketTable.vue'
import PacketDrawer from '@/components/packets/PacketDrawer.vue'
import { compact, portLabel, relTime } from '@/lib/format'
import { now } from '@/composables/now'

/** 'all' (the whole site) or one radio id. */
const radioFilter = ref('all')
const multi = computed(() => live.radios.length > 1)
/** The one radio this view is about: an explicit pick, or the site's only radio. Null while 'all' spans several. */
const scopeRadioId = computed<string | null>(() => (radioFilter.value !== 'all' ? radioFilter.value : multi.value ? null : MAIN_RADIO))
/** That radio's Status, only when the view is about exactly one radio (airtime limits and noise floor are per radio). */
const scopeStatus = computed<Status | null>(() => (scopeRadioId.value ? (live.statuses[scopeRadioId.value] ?? null) : null))
/** The radios whose counters make up this view: every radio for 'all', just the picked one otherwise. */
const scopeIds = computed(() => (radioFilter.value !== 'all' ? [radioFilter.value] : live.radios.map((r) => r.id)))
const scopeStatuses = computed<Status[]>(() => scopeIds.value.map((id) => live.statuses[id]).filter((x): x is Status => !!x))

const airtime = ref<AirtimeStats | null>(null)
const rf = ref<RfStats | null>(null)
const ports = ref<PortStat[]>([])
const selected = ref<Packet | null>(null)
const paused = ref(false)
const frozen = ref<Packet[]>([])
const flashSeq = ref(0)

async function load() {
  const [a, r, p] = await Promise.allSettled([
    api.get<AirtimeStats>(withRadio('/stats/airtime?window=1h', radioFilter.value)),
    api.get<RfStats>(withRadio('/stats/rf?window=1h', radioFilter.value)),
    api.get<PortStat[]>(withRadio('/stats/ports?window=24h', radioFilter.value)),
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
watch(radioFilter, load)

const scopedPackets = computed(() => (radioFilter.value === 'all' ? packets.value : packets.value.filter((p) => p.radio_id === radioFilter.value)))

watch(packets, (_list, old) => {
  flashSeq.value = old?.length ? (old[0]?.seq ?? 0) : 0
})
const visiblePackets = computed(() => (paused.value ? frozen.value : scopedPackets.value).slice(0, 40))
function togglePause() {
  paused.value = !paused.value
  if (paused.value) frozen.value = scopedPackets.value
}

// per-5s deltas from status history for the stat-card trends, summed across whichever radios are in scope
const deltas = (key: 'rx' | 'tx' | 'dupe' | 'relayed' | 'undecryptable') =>
  computed(() => {
    const hists = scopeIds.value.map((id) => live.history[id] ?? []).filter((h) => h.length > 1)
    const len = hists.length ? Math.min(...hists.map((h) => h.length)) : 0
    if (len < 2) return []
    const out: number[] = []
    for (let i = 1; i < len; i++) out.push(hists.reduce((sum, h) => sum + (h[i]![key] - h[i - 1]![key]), 0))
    return out
  })
const rxTrend = deltas('rx')
const txTrend = deltas('tx')
const dupeTrend = deltas('dupe')
const relayTrend = deltas('relayed')
const undecTrend = deltas('undecryptable')

// Counters sum fine across radios (they're just tallies); airtime % and noise floor don't, see below.
const counters = computed(() => {
  const z = { rx: 0, rx_dupe: 0, rx_undecryptable: 0, tx: 0, relayed: 0, relay_cancelled: 0, ack_ok: 0, ack_fail: 0, dropped_duty: 0 }
  for (const st of scopeStatuses.value) for (const k of Object.keys(z) as (keyof typeof z)[]) z[k] += st.counters[k]
  return z
})
const radioErrors = computed(() => scopeStatuses.value.reduce((s, st) => s + st.radio.errors, 0))
const haveStatus = computed(() => scopeStatuses.value.length > 0)

const ackPct = computed(() => {
  const c = counters.value
  if (!haveStatus.value || c.ack_ok + c.ack_fail === 0) return null
  return (c.ack_ok / (c.ack_ok + c.ack_fail)) * 100
})

/** A node/identity's radio(s) put it in the current filter (unknown heard_by is kept rather than hidden). */
const inScope = (radioIds?: string[] | string) => {
  if (radioFilter.value === 'all') return true
  return Array.isArray(radioIds) ? radioIds.length === 0 || radioIds.includes(radioFilter.value) : radioIds === undefined || radioIds === radioFilter.value
}
const nodesActive = computed(() => Object.values(live.nodes).filter((n) => !n.local && now.value - n.last_heard < 2 * 3600_000 && inScope(n.heard_by)).length)
const nodesTotal = computed(() => Object.values(live.nodes).filter((n) => !n.local && inScope(n.heard_by)).length)
const identitiesInScope = computed(() => live.identities.filter((i) => inScope(i.radio_id)))

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

// Noise floor and its trend only mean something for one radio: with several in scope they're shown
// per radio in the table below instead.
const noiseSeries = computed(() => {
  const id = scopeRadioId.value
  if (!id) return []
  const hist = rf.value?.points.map((p) => p.noise_floor_dbm) ?? []
  return [...hist.slice(-40), ...(live.history[id] ?? []).map((h) => h.noise)]
})

const portRows = computed(() =>
  [...ports.value].sort((a, b) => b.rx + b.tx - (a.rx + a.tx)).slice(0, 7).map((p) => ({ label: portLabel(p.port), values: [p.rx, p.tx] })),
)

const lastPacket = computed(() => scopedPackets.value[0])
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Dashboard</h2>
        <p class="page-sub">
          {{ identitiesInScope.length }} {{ identitiesInScope.length === 1 ? 'identity' : 'identities' }}
          {{ !multi ? 'on one modem' : radioFilter === 'all' ? `across ${live.radios.length} radios` : `on ${radioName(radioFilter)}` }}
          · {{ nodesActive }} of {{ nodesTotal }} nodes heard in 2 h · last packet {{ lastPacket ? relTime(lastPacket.time, now) : '—' }}
        </p>
      </div>
      <RadioFilter v-model="radioFilter" id="dashboard-radio-filter" />
    </div>

    <div class="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-6">
      <StatCard label="Received" :value="haveStatus ? compact(counters.rx) : '—'" :sub="haveStatus ? `${radioErrors} CRC errors` : ''" :trend="rxTrend" color="var(--s1)" />
      <StatCard label="Transmitted" :value="haveStatus ? compact(counters.tx) : '—'" :sub="haveStatus ? `${counters.dropped_duty} duty drops` : ''" :trend="txTrend" color="var(--s3)" />
      <StatCard
        label="Duplicates"
        :value="haveStatus ? compact(counters.rx_dupe) : '—'"
        :sub="haveStatus && counters.rx ? `${((counters.rx_dupe / counters.rx) * 100).toFixed(0)}% of RX` : ''"
        :trend="dupeTrend"
        color="var(--ink-3)"
      />
      <StatCard label="Relayed" :value="haveStatus ? compact(counters.relayed) : '—'" :sub="haveStatus ? `${counters.relay_cancelled} cancelled` : ''" :trend="relayTrend" color="var(--s4)" />
      <StatCard
        label="ACK success"
        :value="ackPct === null ? '—' : `${ackPct.toFixed(0)}%`"
        :sub="haveStatus ? `${counters.ack_ok} ok · ${counters.ack_fail} failed` : ''"
        :tone="ackPct !== null && ackPct < 80 ? 'warn' : ''"
      />
      <StatCard
        label="Undecryptable"
        :value="haveStatus ? compact(counters.rx_undecryptable) : '—'"
        :sub="haveStatus && counters.rx ? `${((counters.rx_undecryptable / counters.rx) * 100).toFixed(0)}% of RX` : ''"
        :trend="undecTrend"
        color="var(--s8)"
      />
    </div>

    <RadioOverviewTable v-if="multi && radioFilter === 'all'" />

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
            <span v-if="scopeStatus" class="inline-flex items-center gap-1.5"><span class="h-0.5 w-3 rounded bg-bad" />Duty limit</span>
          </div>
        </div>
        <div class="px-2 pb-3 sm:px-3">
          <TimeChart
            v-if="airChart"
            :times="airChart.times"
            :series="airChart.series"
            :height="240"
            :format="(v: number) => `${v.toFixed(0)}%`"
            :ref-line="scopeStatus ? { value: scopeStatus.airtime.duty_limit_pct, label: `${scopeStatus.airtime.duty_limit_pct}% duty cycle` } : undefined"
          />
          <div v-else class="mx-3 h-[240px] animate-pulse rounded-xl bg-sunken" />
        </div>
      </section>

      <div class="grid gap-4">
        <section v-if="scopeRadioId" class="card">
          <div class="card-head">
            <h3 class="card-title">Noise floor</h3>
            <span class="text-2xs text-ink-3">last hour + live</span>
          </div>
          <div class="px-4 pb-4 sm:px-5">
            <div class="flex items-end gap-4">
              <div>
                <div class="text-[26px] font-semibold leading-none tracking-tight tabular-nums">{{ scopeStatus?.radio.noise_floor_dbm ?? '—' }}<span class="ml-1 text-sm font-normal text-ink-3">dBm</span></div>
                <div class="mt-1.5 text-xs text-ink-3">channel util {{ scopeStatus?.airtime.channel_util_pct.toFixed(1) ?? '—' }}%</div>
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
          <p class="card-sub">
            Newest first · click a row for the header byte map and decoded payload
            <template v-if="radioFilter !== 'all'"> · {{ radioName(radioFilter) }} only</template>
          </p>
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
