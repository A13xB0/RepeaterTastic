<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Cable, Cloud, Network, RefreshCw } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Link } from '@/api/types'
import Toggle from '@/components/ui/Toggle.vue'
import { toast, toastError } from '@/composables/toast'
import { compact } from '@/lib/format'
import { refreshStatus } from '@/store/live'

const links = ref<Link[]>([])
const modeLabels: Record<string, string> = { gateway: 'gateway', uplink_only: 'uplink only', map_only: 'map only', monitor: 'monitor', bridge: 'bridge' }
const loading = ref(false)

const meta: Record<string, { label: string; icon: typeof Cable; about: string }> = {
  udp_multicast: { label: 'UDP multicast', icon: Network, about: 'Encrypted MeshPackets on the LAN, the same format native meshtasticd uses. No RF airtime.' },
  host_link: { label: 'Host link', icon: Cable, about: 'Authenticated MeshPacket stream to another RepeaterTastic site.' },
  mqtt: { label: 'MQTT', icon: Cloud, about: 'A broker connection. Its mode, channels and gateway identity are set under Configuration → MQTT.' },
}

async function load() {
  loading.value = true
  try {
    links.value = await api.get<Link[]>('/links')
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}

const groupDraft = ref<Record<string, string>>({})
async function saveGroup(l: Link) {
  try {
    const updated = await api.patch<Link & { restart_required?: boolean }>(`/links/${enc(l.name)}`, { group: groupDraft.value[l.name] ?? '' })
    Object.assign(l, updated)
    delete groupDraft.value[l.name]
    refreshStatus().catch(() => {})
    toast('Group saved')
  } catch (e) {
    toastError(e)
  }
}

async function setEnabled(l: Link, enabled: boolean) {
  try {
    const updated = await api.patch<Link>(`/links/${enc(l.name)}`, { enabled })
    Object.assign(l, updated)
    refreshStatus().catch(() => {})
    toast(`${l.name} ${enabled ? 'enabled' : 'disabled'}`)
  } catch (e) {
    toastError(e)
  }
}

let timer: number | undefined
onMounted(() => {
  load()
  timer = window.setInterval(load, 10_000)
})
onBeforeUnmount(() => clearInterval(timer))
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Links</h2>
        <p class="page-sub">Non-RF interfaces. Each has its own packet history entry and never uses airtime.</p>
      </div>
      <button type="button" class="btn btn-sm" :disabled="loading" @click="load"><RefreshCw :class="['size-3.5', loading && 'animate-spin']" />Refresh</button>
    </div>

    <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <section v-for="l in links" :key="l.name" class="card flex flex-col p-4 sm:p-5">
        <div class="flex items-start gap-3">
          <span class="flex size-10 shrink-0 items-center justify-center rounded-xl bg-sunken text-ink-2">
            <component :is="meta[l.type]?.icon ?? Cable" class="size-5" />
          </span>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <h3 class="truncate text-[14px] font-semibold">{{ l.connection ?? l.name }}</h3>
              <span
                :class="['chip', !l.enabled ? 'bg-ink-3/14 text-ink-3' : l.connected ? 'bg-ok/14 text-ok' : 'bg-warn/15 text-warn']"
              >
                <span :class="['dot size-1.5', !l.enabled ? 'bg-ink-3' : l.connected ? 'bg-ok' : 'bg-warn']" />
                {{ !l.enabled ? 'disabled' : l.connected ? 'connected' : 'reconnecting' }}
              </span>
            </div>
            <div class="text-xs text-ink-3">{{ meta[l.type]?.label ?? l.type }}<template v-if="l.mode"> · <span :class="l.mode === 'bridge' ? 'text-warn' : ''">{{ modeLabels[l.mode] ?? l.mode }}</span></template></div>
          </div>
          <Toggle v-if="l.type !== 'mqtt'" :model-value="l.enabled" :label="`Enable ${l.name}`" @update:model-value="setEnabled(l, $event)" />
          <RouterLink v-else class="btn btn-sm" :to="{ name: 'config', params: { tab: 'mqtt' } }">Settings</RouterLink>
        </div>
        <p v-if="l.detail" class="mono mt-3 truncate rounded-lg bg-raised px-2.5 py-1.5 text-xs text-ink-2" :title="l.detail">{{ l.detail }}</p>
        <p class="mt-3 flex-1 text-xs leading-relaxed text-ink-3">{{ meta[l.type]?.about }}</p>
        <form v-if="l.type === 'udp_multicast'" class="mt-3 flex items-end gap-2" @submit.prevent="saveGroup(l)">
          <div class="min-w-0 flex-1">
            <label class="label" :for="`grp-${l.name}`">Multicast group</label>
            <input :id="`grp-${l.name}`" class="input mono !h-8" placeholder="239.0.0.69:4403" :value="groupDraft[l.name] ?? l.group ?? ''" @input="groupDraft[l.name] = ($event.target as HTMLInputElement).value" />
          </div>
          <button type="submit" class="btn btn-sm" :disabled="groupDraft[l.name] === undefined || groupDraft[l.name] === (l.group ?? '')">Save</button>
        </form>
        <dl v-if="l.type === 'mqtt' && l.enabled" class="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
          <dt class="text-ink-3">Topic root</dt><dd class="mono truncate">{{ l.root || '—' }}{{ l.tls ? ' · TLS' : '' }}</dd>
          <dt class="text-ink-3">Gateway</dt><dd class="mono truncate">{{ l.gateway_id ?? l.gateway }}</dd>
          <dt class="text-ink-3">Uplink</dt><dd class="truncate">{{ l.mode === 'map_only' ? 'map report only' : l.uplink?.length ? `${l.uplink.join(', ')} · ${l.format}` : 'none' }}</dd>
          <dt class="text-ink-3">Downlink</dt><dd class="truncate">{{ l.downlink?.length ? l.downlink.join(', ') : 'none' }}</dd>
          <dt class="text-ink-3">Our packets</dt><dd>{{ l.ok_to_mqtt ? 'OK to uplink' : 'not OK to uplink' }}</dd>
          <dt class="text-ink-3">Relay</dt><dd :class="l.relay_mqtt ? 'text-warn' : ''">{{ l.relay_mqtt ? 'rebroadcasts its traffic on air' : 'never puts its traffic on air' }}</dd>
          <dt class="text-ink-3">Other connections</dt><dd>{{ l.cross_link ? 'may pass traffic' : 'kept separate' }}</dd>
          <dt class="text-ink-3">Map report</dt><dd>{{ l.map_report ? 'on' : 'off' }}</dd>
        </dl>
        <div :class="['mt-4 grid gap-2 border-t border-line-soft pt-3', l.type === 'mqtt' ? 'grid-cols-3' : 'grid-cols-2']">
          <div>
            <div class="text-2xs text-ink-3">Received</div>
            <div class="text-lg font-semibold tabular-nums">{{ compact(l.rx) }}</div>
          </div>
          <div>
            <div class="text-2xs text-ink-3">Sent</div>
            <div class="text-lg font-semibold tabular-nums">{{ compact(l.tx) }}</div>
          </div>
          <div v-if="l.type === 'mqtt'" title="Packets refused: not encrypted, wrong channel or over a rate limit">
            <div class="text-2xs text-ink-3">Dropped</div>
            <div class="text-lg font-semibold tabular-nums">{{ compact(l.dropped ?? 0) }}</div>
          </div>
        </div>
      </section>
    </div>
    <div v-if="!links.length && !loading" class="card empty">No links configured. Add them under <span class="mono">links:</span> in the config file.</div>
  </div>
</template>
