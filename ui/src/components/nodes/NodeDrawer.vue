<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { BatteryMedium, CircleAlert, IdCard, Lock, MapPin, MessagesSquare, Route as RouteIcon, Trash } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { TracerouteEvent } from '@/api/types'
import Drawer from '@/components/ui/Drawer.vue'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, nodeByLastByte, nodeLabel, on } from '@/store/live'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { now } from '@/composables/now'
import { dateTime, hwLabel, relTime, roleLabel, snrClass } from '@/lib/format'

const props = defineProps<{ nodeId: string | null }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()

const node = computed(() => (props.nodeId ? live.nodes[props.nodeId] : undefined))
// With several radios: what each one knows about this node.
const sightings = ref<{ radio_id: string; radio_name: string; last_heard: number; snr: number; hops_away: number; via_mqtt: boolean }[]>([])
watch(
  () => [props.nodeId, live.radios.length] as const,
  async ([id, radios]) => {
    sightings.value = []
    if (!id || radios < 2) return
    try {
      sightings.value = await api.get(`/nodes/${enc(id)}/sightings`)
    } catch {
      /* optional */
    }
  },
  { immediate: true },
)
const senders = computed(() => live.identities.filter((i) => i.enabled))
const from = ref('')
const traces = ref<(TracerouteEvent & { time: number })[]>([])
const pending = ref(false)
const nodeinfoBusy = ref(false)
let timeout: number | undefined

watch(
  () => props.nodeId,
  () => {
    traces.value = []
    pending.value = false
    clearTimeout(timeout)
    if (!from.value || !senders.value.some((s) => s.node_id === from.value)) from.value = senders.value.find((s) => !s.is_relay)?.node_id ?? senders.value[0]?.node_id ?? ''
  },
  { immediate: true },
)

const off = on('traceroute', (t) => {
  if (t.target !== props.nodeId) return
  pending.value = false
  clearTimeout(timeout)
  traces.value = [{ ...t, time: Date.now() }, ...traces.value].slice(0, 5)
})
onBeforeUnmount(() => {
  off()
  clearTimeout(timeout)
})

async function traceroute() {
  if (!node.value || !from.value) return
  pending.value = true
  try {
    await api.post(`/nodes/${enc(node.value.node_id)}/traceroute`, { from: from.value })
    timeout = window.setTimeout(() => {
      if (!pending.value) return
      pending.value = false
      traces.value = [{ identity: from.value, target: props.nodeId!, route: [], snr_towards: [], route_back: [], snr_back: [], error: 'no response within 60 s', time: Date.now() }, ...traces.value]
    }, 60_000)
  } catch (e) {
    pending.value = false
    toastError(e)
  }
}

async function requestNodeinfo() {
  if (!node.value || !from.value) return
  nodeinfoBusy.value = true
  try {
    await api.post(`/nodes/${enc(node.value.node_id)}/request-nodeinfo`, { from: from.value })
    toast(`NodeInfo requested from ${node.value.short_name}`)
  } catch (e) {
    toastError(e)
  } finally {
    nodeinfoBusy.value = false
  }
}

async function forget() {
  const n = node.value
  if (!n) return
  if (!(await confirmDialog({ title: `Forget ${n.long_name}?`, body: 'The node is removed from the shared node DB. It will reappear the next time it is heard.', confirm: 'Forget node', danger: true }))) return
  try {
    await api.del(`/nodes/${enc(n.node_id)}`)
    delete live.nodes[n.node_id]
    emit('close')
    toast(`${n.short_name} forgotten`)
  } catch (e) {
    toastError(e)
  }
}

function dm() {
  if (!node.value) return
  // Send from the identity picked above, the relay persona included.
  const sender = from.value || live.identities.find((i) => !i.is_relay && i.enabled)?.node_id
  router.push({ name: 'chat', params: { identity: sender, conversation: `dm:${node.value.node_id}` } })
}

function hopChain(t: TracerouteEvent, back = false) {
  const route = back ? t.route_back : t.route
  const snr = back ? t.snr_back : t.snr_towards
  const ends = back ? [t.target, t.identity] : [t.identity, t.target]
  const ids = [ends[0]!, ...route, ends[1]!]
  return ids.map((id, i) => ({ id, label: nodeLabel(id), snr: i === 0 ? null : (snr[i - 1] ?? null) }))
}
const nextHop = computed(() => (node.value?.next_hop ? nodeByLastByte(node.value.next_hop) : undefined))
</script>

<template>
  <Drawer :open="!!node" @close="emit('close')">
    <template #header>
      <div v-if="node" class="flex items-center gap-3">
        <NodeAvatar :id="node.node_id" :short="node.short_name" size="lg" :local="node.local" />
        <div class="min-w-0">
          <h2 class="truncate text-[15px] font-semibold tracking-tight">{{ node.long_name }}</h2>
          <div class="flex items-center gap-1 text-[13px] text-ink-3">
            <span class="mono">{{ node.node_id }}</span><CopyButton :text="node.node_id" label="Node id" />
            <span v-if="node.local" class="chip bg-brand/14 text-brand">local</span>
            <span v-if="node.via_mqtt" class="chip bg-ink-3/14 text-ink-3">MQTT</span>
          </div>
        </div>
      </div>
    </template>

    <div v-if="node" class="space-y-6">
      <div class="grid grid-cols-3 gap-2">
        <div class="rounded-xl border border-line-soft bg-raised px-3 py-2">
          <div class="text-2xs text-ink-3">SNR</div>
          <div :class="['text-base font-semibold tabular-nums', snrClass(node.snr)]">{{ node.snr?.toFixed(1) ?? '—' }}<span class="text-xs font-normal text-ink-3"> dB</span></div>
        </div>
        <div class="rounded-xl border border-line-soft bg-raised px-3 py-2">
          <div class="text-2xs text-ink-3">RSSI</div>
          <div class="text-base font-semibold tabular-nums">{{ node.rssi ?? '—' }}<span class="text-xs font-normal text-ink-3"> dBm</span></div>
        </div>
        <div class="rounded-xl border border-line-soft bg-raised px-3 py-2">
          <div class="text-2xs text-ink-3">Hops away</div>
          <div class="text-base font-semibold tabular-nums">{{ node.hops_away ?? '—' }}</div>
        </div>
      </div>

      <div v-if="sightings.length > 1" class="rounded-xl border border-line-soft px-3 py-2">
        <div class="eyebrow mb-1">Heard by radio</div>
        <ul class="grid gap-1 text-xs">
          <li v-for="sg in sightings" :key="sg.radio_id" class="flex flex-wrap items-baseline justify-between gap-2">
            <span class="font-medium">{{ sg.radio_name }}</span>
            <span class="tabular-nums text-ink-3">{{ relTime(sg.last_heard, now) }} · {{ sg.hops_away < 0 ? 'hops ?' : sg.hops_away === 0 ? 'direct' : `${sg.hops_away} hop${sg.hops_away === 1 ? '' : 's'}` }} · SNR {{ sg.snr.toFixed(1) }}{{ sg.via_mqtt ? ' · MQTT' : '' }}</span>
          </li>
        </ul>
      </div>

      <dl class="kv">
        <dt>Last heard</dt>
        <dd :title="dateTime(node.last_heard)">{{ node.local ? 'local identity' : relTime(node.last_heard, now) }}</dd>
        <dt>Role</dt>
        <dd>{{ roleLabel(node.role) }}</dd>
        <dt>Hardware</dt>
        <dd>{{ hwLabel(node.hw_model) }}</dd>
        <dt>Public key</dt>
        <dd>
          <span v-if="node.has_public_key" class="inline-flex items-center gap-1 text-ok"><Lock class="size-3.5" />known, DMs use PKI</span>
          <span v-else class="text-ink-3">not yet received</span>
        </dd>
        <dt>Next hop</dt>
        <dd>
          <template v-if="node.next_hop">
            <span class="mono">0x{{ node.next_hop.toString(16).padStart(2, '0') }}</span>
            <span class="text-ink-3"> {{ nextHop ? `${nextHop.short_name} ${nextHop.long_name}` : 'ambiguous' }}</span>
          </template>
          <span v-else class="text-ink-3">flood</span>
        </dd>
        <template v-if="node.position">
          <dt>Position</dt>
          <dd>
            <a
              class="inline-flex items-center gap-1 text-brand hover:underline"
              :href="`https://www.openstreetmap.org/?mlat=${node.position.lat}&mlon=${node.position.lon}#map=14/${node.position.lat}/${node.position.lon}`"
              target="_blank"
              rel="noopener"
            ><MapPin class="size-3.5" />{{ node.position.lat.toFixed(5) }}, {{ node.position.lon.toFixed(5) }}</a>
            <span class="text-ink-3"> · {{ node.position.alt }} m</span>
          </dd>
        </template>
        <template v-if="node.telemetry">
          <dt>Device</dt>
          <dd class="tabular-nums">
            <span class="inline-flex items-center gap-1"><BatteryMedium class="size-3.5 text-ink-3" />{{ node.telemetry.battery > 100 ? 'powered' : `${node.telemetry.battery}%` }}</span>
            · {{ node.telemetry.voltage.toFixed(2) }} V
          </dd>
          <dt>Channel util</dt>
          <dd class="tabular-nums">{{ node.telemetry.channel_util.toFixed(1) }}% · air TX {{ node.telemetry.air_util_tx.toFixed(2) }}%</dd>
        </template>
        <template v-if="node.known_by?.length">
          <dt>Known by</dt>
          <dd class="flex flex-wrap gap-1">
            <span v-for="id in node.known_by" :key="id" class="chip bg-sunken text-ink-2">{{ nodeLabel(id).short }}</span>
          </dd>
        </template>
      </dl>

      <section v-if="!node.local" class="rounded-xl border border-line-soft p-3.5">
        <h3 class="eyebrow mb-2">Actions</h3>
        <label class="label" for="nd-from">Send from</label>
        <select id="nd-from" v-model="from" class="input">
          <option v-for="s in senders" :key="s.node_id" :value="s.node_id">{{ s.long_name }} ({{ s.node_id }}){{ s.is_relay ? ' · relay persona' : '' }}</option>
        </select>
        <div class="mt-3 flex flex-wrap gap-2">
          <button type="button" class="btn btn-sm" :disabled="pending || !from" @click="traceroute"><Spinner v-if="pending" /><RouteIcon v-else class="size-3.5" />Traceroute</button>
          <button type="button" class="btn btn-sm" :disabled="nodeinfoBusy || !from" @click="requestNodeinfo"><Spinner v-if="nodeinfoBusy" /><IdCard v-else class="size-3.5" />Request NodeInfo</button>
          <button type="button" class="btn btn-sm" @click="dm"><MessagesSquare class="size-3.5" />Message</button>
          <button type="button" class="btn btn-sm btn-ghost ml-auto hover:!text-bad" @click="forget"><Trash class="size-3.5" />Forget</button>
        </div>

        <div v-if="pending" class="mt-3 flex items-center gap-2 text-xs text-ink-3"><Spinner />Waiting for the route reply…</div>
        <div v-for="t in traces" :key="t.time" class="mt-3 rounded-lg bg-raised p-3">
          <div class="mb-2 flex items-center justify-between text-2xs text-ink-3">
            <span>from {{ nodeLabel(t.identity).long }}</span><span>{{ relTime(t.time, now) }}</span>
          </div>
          <div v-if="t.error" class="flex items-center gap-1.5 text-[13px] text-bad"><CircleAlert class="size-4" />{{ t.error }}</div>
          <template v-else>
            <div v-for="dir in ['towards', 'back'] as const" :key="dir" class="mb-1.5 last:mb-0">
              <div class="mb-1 text-2xs font-medium text-ink-3">{{ dir === 'towards' ? 'Route there' : 'Route back' }}</div>
              <div class="flex flex-wrap items-center gap-y-1">
                <template v-for="(h, i) in hopChain(t, dir === 'back')" :key="i">
                  <span v-if="i" class="flex items-center px-1 text-2xs text-ink-3">
                    <span class="mx-0.5 h-px w-3 bg-line" /><span :class="snrClass(h.snr)">{{ h.snr?.toFixed(1) }}</span><span class="mx-0.5 h-px w-3 bg-line" />
                  </span>
                  <span :class="['chip h-6 text-xs', h.label.local ? 'bg-brand/14 text-brand' : 'bg-surface-solid text-ink']" :title="h.label.long">{{ h.label.short }}</span>
                </template>
              </div>
            </div>
          </template>
        </div>
      </section>
    </div>
  </Drawer>
</template>
