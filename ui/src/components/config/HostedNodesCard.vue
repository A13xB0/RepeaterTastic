<script setup lang="ts">
// Configuration → Experimental: run the relay persona on meshtasticd, and see what runs.
import { onMounted, ref } from 'vue'
import { api } from '@/api/client'
import type { HostedSettings } from '@/api/types'
import Spinner from '@/components/ui/Spinner.vue'
import Toggle from '@/components/ui/Toggle.vue'
import { refreshStatus } from '@/store/live'
import { toast, toastError } from '@/composables/toast'

const state = ref<HostedSettings | null>(null)
const via = ref<'exec' | 'docker'>('exec')
const binary = ref('')
const image = ref('meshtastic/meshtasticd:2.8.0.47db0e3-alpha-debian')
const portBase = ref(4500)
const persona = ref(false)
const busy = ref(false)
const error = ref('')

function load(s: HostedSettings) {
  state.value = s
  persona.value = s.persona
  via.value = s.docker_image ? 'docker' : 'exec'
  binary.value = s.meshtasticd
  if (s.docker_image) image.value = s.docker_image
  portBase.value = s.port_base
}

onMounted(async () => {
  try {
    load(await api.get<HostedSettings>('/hosted'))
  } catch (e) {
    toastError(e)
  }
})

async function save() {
  busy.value = true
  error.value = ''
  try {
    const s = await api.put<HostedSettings>('/hosted', {
      persona: persona.value,
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
  <div v-else class="rounded-xl border border-line-soft px-4 py-3.5">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-2 text-[13px] font-medium">
          Relay persona on meshtasticd
          <span :class="['chip', state.persona ? 'bg-ok/14 text-ok' : 'bg-ink-3/12 text-ink-3']">{{ state.persona ? 'on' : 'off' }}</span>
        </div>
        <p class="mt-1 text-xs text-ink-3">
          Each modem or HAT radio's relay runs as a real Meshtastic node: a meshtasticd {{ state.min_version }}+ on a simulated radio, still transmitting on this radio at zero hops.
          Takes effect at the next restart. Your identities stay in RepeaterTastic for now.
        </p>
      </div>
      <Toggle :model-value="persona" :disabled="busy" label="Relay persona on meshtasticd" @update:model-value="persona = $event" />
    </div>
    <div v-if="persona" class="mt-3 space-y-3">
      <div class="flex flex-wrap items-center gap-2">
        <div class="seg" role="group" aria-label="How to run meshtasticd">
          <button type="button" :aria-pressed="via === 'exec'" @click="via = 'exec'">Installed</button>
          <button type="button" :aria-pressed="via === 'docker'" @click="via = 'docker'">Docker</button>
        </div>
        <input v-if="via === 'exec'" id="hosted-bin" v-model="binary" class="input h-8 mono min-w-0 flex-1 text-xs" placeholder="meshtasticd (on PATH) or /usr/bin/meshtasticd" spellcheck="false" aria-label="meshtasticd program" />
        <input v-else id="hosted-image" v-model="image" class="input h-8 mono min-w-0 flex-1 text-xs" spellcheck="false" aria-label="meshtasticd image" />
      </div>
      <div class="flex flex-wrap items-end gap-3">
        <div>
          <label class="label" for="hosted-port">API ports from</label>
          <input id="hosted-port" v-model.number="portBase" type="number" min="1024" max="64000" class="input h-8 w-28 text-xs" />
        </div>
        <p class="hint mb-1 flex-1"><template v-if="via === 'exec'">meshtasticd listens on every interface: firewall ports {{ portBase }} onwards on a shared network, or use Docker.</template><template v-else>Docker keeps the ports on this machine.</template></p>
      </div>
    </div>
    <div v-if="state.instances.length" class="scroll-thin mt-3 overflow-x-auto rounded-lg border border-line-soft">
      <table class="tbl text-xs">
        <thead><tr><th>Node</th><th>Radio</th><th>meshtasticd</th><th>Port</th><th>State</th></tr></thead>
        <tbody>
          <tr v-for="x in state.instances" :key="x.name">
            <td><span class="font-medium">{{ x.role === 'persona' ? 'Relay persona' : x.name }}</span> <span class="mono text-ink-3">{{ x.node_id }}</span></td>
            <td>{{ x.radio }}</td>
            <td :title="x.launcher">{{ x.firmware || '—' }} <span class="text-ink-3">· {{ x.launcher.startsWith('docker ') ? 'Docker' : 'installed' }}</span></td>
            <td class="mono">{{ x.port }}</td>
            <td><span :class="['chip', x.connected ? 'bg-ok/14 text-ok' : 'bg-warn/15 text-warn']">{{ x.connected ? 'running' : x.running ? 'starting' : 'stopped' }}</span><span v-if="x.restarts" class="ml-1 text-ink-3">{{ x.restarts }} restarts</span></td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-if="error" class="mt-2 text-xs text-bad">{{ error }}</p>
    <div class="mt-3 flex justify-end">
      <button class="btn btn-sm btn-primary" :disabled="busy" @click="save"><Spinner v-if="busy" />Save</button>
    </div>
  </div>
</template>
