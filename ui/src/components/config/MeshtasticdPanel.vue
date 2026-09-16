<script setup lang="ts">
// Configuration → meshtasticd: which meshtasticd runs the nodes, and how each one is doing.
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '@/api/client'
import type { HostedSettings, RadiosResponse, Runtimes } from '@/api/types'
import Spinner from '@/components/ui/Spinner.vue'
import NodesHealthChip from '@/components/layout/NodesHealthChip.vue'
import { live, refreshStatus } from '@/store/live'
import { toast, toastError } from '@/composables/toast'

const state = ref<HostedSettings | null>(null)
const via = ref<'exec' | 'docker'>('exec')
const binary = ref('')
const image = ref('meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian')
const portBase = ref(4500)
const busy = ref(false)
const runtimes = ref<Runtimes | null>(null)
// Radios on a Meshtastic board keep the board as their relay: only their identities run here.
const radios = ref<RadiosResponse['radios']>([])
const boardRadios = computed(() => radios.value.filter((r) => r.driver === 'meshtastic'))
const radioName = (id: string) => radios.value.find((r) => r.id === id)?.name ?? id
const error = ref('')
const health = computed(() => live.status?.nodes)

function load(s: HostedSettings) {
  state.value = s
  via.value = s.docker_image ? 'docker' : 'exec'
  binary.value = s.meshtasticd
  if (s.docker_image) image.value = s.docker_image
  portBase.value = s.port_base
}

// The instance table follows the nodes as they start and stop.
let timer: ReturnType<typeof setInterval> | undefined
async function poll() {
  try {
    const s = await api.get<HostedSettings>('/hosted')
    if (state.value) state.value.instances = s.instances
  } catch {
    /* next time */
  }
}

onMounted(async () => {
  try {
    load(await api.get<HostedSettings>('/hosted'))
    api.get<RadiosResponse>('/radios').then((r) => (radios.value = r.radios)).catch(() => {})
    api.get<Runtimes>('/setup/runtimes').then((r) => {
      runtimes.value = r
      if (!state.value?.meshtasticd && !state.value?.docker_image && !r.meshtasticd.ok && r.docker.ok) via.value = 'docker'
    }).catch(() => {})
    timer = setInterval(poll, 5000)
  } catch (e) {
    toastError(e)
  }
})
onUnmounted(() => clearInterval(timer))

async function save() {
  busy.value = true
  error.value = ''
  try {
    const s = await api.put<HostedSettings>('/hosted', {
      meshtasticd: via.value === 'exec' ? binary.value : '',
      docker_image: via.value === 'docker' ? image.value : '',
      port_base: portBase.value,
    })
    load(s)
    refreshStatus().catch(() => {})
    toast(s.restart_required ? 'Saved. Restart to apply.' : 'Saved')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div v-if="!state" class="h-24 animate-pulse rounded-xl bg-sunken" />
  <div v-else class="space-y-4">
    <div class="rounded-xl border border-line-soft px-4 py-3.5">
      <div class="flex flex-wrap items-center gap-2 text-[13px] font-medium">
        Nodes
        <NodesHealthChip v-if="health" :health="health" />
      </div>
      <p class="mt-1 text-xs text-ink-3">
        Every relay persona and identity is a real Meshtastic node: a meshtasticd {{ state.min_version }}+ on a simulated radio, transmitting through this radio.
        Each keeps its saved key, so node numbers, channels and chats stay.
        <template v-if="boardRadios.length"> {{ boardRadios.map((r) => r.name).join(', ') }} {{ boardRadios.length === 1 ? 'is a board' : 'are boards' }} running Meshtastic firmware: the board is the relay, and its identities run one hop behind it.</template>
      </p>
      <ul v-if="health?.problems.length" class="mt-2 space-y-0.5 text-xs" :class="health.state === 'error' ? 'text-bad' : 'text-warn'">
        <li v-for="p in health.problems" :key="p">{{ p }}</li>
      </ul>
    </div>

    <div class="rounded-xl border border-line-soft px-4 py-3.5">
      <div class="text-[13px] font-medium">meshtasticd program</div>
      <p v-if="runtimes" class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-2xs">
        <span :class="runtimes.meshtasticd.ok ? 'text-ok' : 'text-ink-3'">Installed meshtasticd: {{ runtimes.meshtasticd.ok ? `${runtimes.meshtasticd.version} (${runtimes.meshtasticd.path})` : runtimes.meshtasticd.error }}</span>
        <span :class="runtimes.docker.ok ? 'text-ok' : 'text-ink-3'">Docker: {{ runtimes.docker.ok ? `${runtimes.docker.version}, image ${runtimes.docker.image_present ? 'downloaded' : 'not downloaded yet'}` : runtimes.docker.error }}</span>
      </p>
      <div class="mt-3 flex flex-wrap items-center gap-2">
        <div class="seg" role="group" aria-label="How to run meshtasticd">
          <button type="button" :aria-pressed="via === 'exec'" @click="via = 'exec'">Installed</button>
          <button type="button" :aria-pressed="via === 'docker'" :disabled="!!runtimes && !runtimes.docker.ok" @click="via = 'docker'">Docker</button>
        </div>
        <input v-if="via === 'exec'" id="hosted-bin" v-model="binary" class="input h-8 mono min-w-0 flex-1 text-xs" :placeholder="runtimes && !runtimes.meshtasticd.found ? 'not on the PATH: enter where it is' : 'meshtasticd (on PATH) or /usr/bin/meshtasticd'" spellcheck="false" aria-label="meshtasticd program" />
        <input v-else id="hosted-image" v-model="image" class="input h-8 mono min-w-0 flex-1 text-xs" spellcheck="false" aria-label="meshtasticd image" />
      </div>
      <div class="mt-3 flex flex-wrap items-end gap-3">
        <div>
          <label class="label" for="hosted-port">API ports from</label>
          <input id="hosted-port" v-model.number="portBase" type="number" min="1024" max="64000" class="input h-8 w-28 text-xs" />
        </div>
        <p class="hint mb-1 flex-1"><template v-if="via === 'exec'">meshtasticd listens on every interface: firewall ports {{ portBase }}–{{ portBase + 99 }} (100 per radio) on a shared network, or use Docker.</template><template v-else>Docker keeps the ports on this machine.</template> Changes apply at the next restart.</p>
      </div>
      <p v-if="error" class="mt-2 text-xs text-bad">{{ error }}</p>
      <div class="mt-3 flex justify-end">
        <button type="button" class="btn btn-sm btn-primary" :disabled="busy" @click="save"><Spinner v-if="busy" />Save</button>
      </div>
    </div>

    <div v-if="state.instances.length" class="scroll-thin overflow-x-auto rounded-xl border border-line-soft">
      <table class="tbl text-xs">
        <thead><tr><th>Node</th><th>Radio</th><th>meshtasticd</th><th>Port</th><th>State</th></tr></thead>
        <tbody>
          <tr v-for="x in state.instances" :key="x.name">
            <td><span class="font-medium">{{ x.role === 'persona' ? 'Relay persona' : 'Identity' }}</span> <span class="mono text-ink-3">{{ x.node_id }}</span></td>
            <td>{{ radioName(x.radio) }}</td>
            <td :title="x.launcher">{{ x.firmware || '—' }} <span class="text-ink-3">· {{ x.launcher.startsWith('docker ') ? 'Docker' : 'installed' }}</span></td>
            <td class="mono">{{ x.port }}</td>
            <td>
              <span :class="['chip', x.connected ? 'bg-ok/14 text-ok' : x.running && !x.restarts ? 'bg-warn/15 text-warn' : 'bg-bad/12 text-bad']">{{ x.connected ? 'running' : x.running ? 'starting' : 'stopped' }}</span>
              <span v-if="x.restarts" class="ml-1 text-ink-3">{{ x.restarts }} restarts</span>
              <div v-if="x.last_error && !x.connected" class="mt-0.5 max-w-64 truncate text-2xs text-bad" :title="x.last_error">{{ x.last_error }}</div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
