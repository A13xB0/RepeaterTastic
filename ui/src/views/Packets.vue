<script setup lang="ts">
// Packet archive with filters, live prepend and a byte-level detail drawer (after openHop's PacketArchive).
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { markRaw } from 'vue'
import { Radio, RotateCcw } from '@lucide/vue'
import { api, qs, withRadio } from '@/api/client'
import type { Packet, PacketKind } from '@/api/types'
import { live, on } from '@/store/live'
import PacketTable from '@/components/packets/PacketTable.vue'
import PacketDrawer from '@/components/packets/PacketDrawer.vue'
import RadioFilter from '@/components/ui/RadioFilter.vue'
import Toggle from '@/components/ui/Toggle.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { toastError } from '@/composables/toast'
import { portLabel } from '@/lib/format'

const PORTS = ['TEXT_MESSAGE_APP', 'POSITION_APP', 'NODEINFO_APP', 'TELEMETRY_APP', 'ROUTING_APP', 'TRACEROUTE_APP', 'NEIGHBORINFO_APP', 'STORE_FORWARD_APP', 'ADMIN_APP', 'RANGE_TEST_APP', 'WAYPOINT_APP']
const KINDS: PacketKind[] = ['ours', 'delivered', 'relayed', 'dup', 'undecryptable', 'local']
const RANGES = [
  { id: '', label: 'Any time' },
  { id: '15m', label: 'Last 15 min', ms: 15 * 60_000 },
  { id: '1h', label: 'Last hour', ms: 3600_000 },
  { id: '24h', label: 'Last 24 h', ms: 86_400_000 },
]

const filters = ref({ node: '', port: '', kind: '', direction: '', channel: '', q: '', range: '' })
/** 'all' or a radio id; kept outside `filters` so it doesn't count towards the active-filter chip. */
const radioFilter = ref('all')
const list = ref<Packet[]>([])
const loading = ref(false)
const more = ref(true)
const liveOn = ref(true)
const selected = ref<Packet | null>(null)
const flashSeq = ref(0)
const PAGE = 100
const severalRadios = computed(() => live.radios.length > 1)

const channels = computed(() => {
  const set = new Set<string>()
  for (const i of live.identities) for (const c of i.channels) if (c.role !== 'DISABLED') set.add(c.display_name)
  return [...set]
})
const nodes = computed(() => Object.values(live.nodes).sort((a, b) => a.long_name.localeCompare(b.long_name)))

function params(before?: number) {
  const f = filters.value
  const r = RANGES.find((x) => x.id === f.range)
  return qs({ limit: PAGE, before, node: f.node, port: f.port, kind: f.kind, direction: f.direction, channel: f.channel, q: f.q.trim(), since: r?.ms ? Date.now() - r.ms : undefined })
}

async function load(append = false) {
  loading.value = true
  try {
    const before = append ? list.value[list.value.length - 1]?.time : undefined
    const page = (await api.get<Packet[]>(withRadio(`/packets${params(before)}`, radioFilter.value))).map((p) => markRaw(p))
    list.value = append ? [...list.value, ...page] : page
    more.value = page.length === PAGE
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}

let debounce: number | undefined
watch(filters, () => {
  clearTimeout(debounce)
  debounce = window.setTimeout(() => load(), 250)
}, { deep: true })
watch(radioFilter, () => load())
onMounted(() => load())

function matches(p: Packet) {
  const f = filters.value
  if (radioFilter.value !== 'all' && (p.radio_id ?? 'main') !== radioFilter.value) return false
  if (f.node && p.from !== f.node && p.to !== f.node) return false
  if (f.port && p.port !== f.port) return false
  if (f.kind && p.kind !== f.kind) return false
  if (f.direction && p.direction !== f.direction) return false
  if (f.channel && p.channel !== f.channel) return false
  if (f.q.trim() && !p.summary.toLowerCase().includes(f.q.trim().toLowerCase())) return false
  return true
}
const off = on('packet', (p) => {
  if (!liveOn.value || loading.value || !matches(p)) return
  flashSeq.value = list.value[0]?.seq ?? 0
  list.value = [markRaw(p), ...list.value].slice(0, 2000)
})
onBeforeUnmount(() => {
  off()
  clearTimeout(debounce)
})

const active = computed(() => Object.values(filters.value).filter(Boolean).length)
function reset() {
  filters.value = { node: '', port: '', kind: '', direction: '', channel: '', q: '', range: '' }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Packets</h2>
        <p class="page-sub">Every frame the modem heard or sent, newest first · {{ list.length }} loaded</p>
      </div>
      <label class="flex items-center gap-2 text-[13px] text-ink-2">
        <Radio :class="['size-4', liveOn ? 'text-ok' : 'text-ink-3']" />Live
        <Toggle v-model="liveOn" label="Live updates" />
      </label>
    </div>

    <section class="card mb-4 p-3 sm:p-4">
      <div class="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-7">
        <input id="packets-search" v-model="filters.q" aria-label="Search packets" class="input col-span-2 sm:col-span-3 lg:col-span-4 xl:col-span-2" placeholder="Search summary text" />
        <select id="packets-node" v-model="filters.node" aria-label="Node" class="input">
          <option value="">Any node</option>
          <option v-for="n in nodes" :key="n.node_id" :value="n.node_id">{{ n.short_name }} · {{ n.long_name }}</option>
        </select>
        <select id="packets-port" v-model="filters.port" aria-label="Port" class="input">
          <option value="">Any port</option>
          <option v-for="p in PORTS" :key="p" :value="p">{{ portLabel(p) }}</option>
        </select>
        <select id="packets-kind" v-model="filters.kind" aria-label="Kind" class="input">
          <option value="">Any kind</option>
          <option v-for="k in KINDS" :key="k" :value="k">{{ k }}</option>
        </select>
        <select id="packets-channel" v-model="filters.channel" aria-label="Channel" class="input">
          <option value="">Any channel</option>
          <option v-for="c in channels" :key="c" :value="c">{{ c }}</option>
        </select>
        <div class="flex gap-2">
          <select id="packets-range" v-model="filters.range" aria-label="Time range" class="input">
            <option v-for="r in RANGES" :key="r.id" :value="r.id">{{ r.label }}</option>
          </select>
        </div>
      </div>
      <div class="mt-2.5 flex flex-wrap items-center gap-2">
        <div class="seg">
          <button type="button" :aria-pressed="filters.direction === ''" @click="filters.direction = ''">RX + TX</button>
          <button type="button" :aria-pressed="filters.direction === 'rx'" @click="filters.direction = 'rx'">RX</button>
          <button type="button" :aria-pressed="filters.direction === 'tx'" @click="filters.direction = 'tx'">TX</button>
        </div>
        <RadioFilter v-model="radioFilter" id="packets-radio" />
        <button type="button" v-if="active" class="btn btn-sm btn-ghost" @click="reset"><RotateCcw class="size-3.5" />Clear {{ active }} filter{{ active === 1 ? '' : 's' }}</button>
        <Spinner v-if="loading" class="ml-auto text-ink-3" />
      </div>
    </section>

    <section class="card overflow-hidden">
      <PacketTable :packets="list" :flash-seq="flashSeq" :show-radio="severalRadios" @select="selected = $event" />
      <div v-if="list.length" class="flex justify-center border-t border-line-soft p-3">
        <button type="button" v-if="more" class="btn btn-sm" :disabled="loading" @click="load(true)"><Spinner v-if="loading" />Load older</button>
        <span v-else class="text-xs text-ink-3">End of archive</span>
      </div>
    </section>

    <PacketDrawer :packet="selected" @close="selected = null" />
  </div>
</template>
