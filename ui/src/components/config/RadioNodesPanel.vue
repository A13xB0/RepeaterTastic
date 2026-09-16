<script setup lang="ts">
// Where a radio's relay persona and identities run (RepeaterTastic or meshtasticd, which version)
// and how far they are from the air. Every node on a radio we drive transmits itself: 0 hops.
import { computed, ref, watch } from 'vue'
import { api, enc } from '@/api/client'
import type { HostedInstance, HostedSettings, Identity } from '@/api/types'

const props = defineProps<{ radioId: string }>()

const identities = ref<Identity[]>([])
const instances = ref<HostedInstance[]>([])
const loaded = ref(false)

watch(
  () => props.radioId,
  async (id) => {
    loaded.value = false
    const [ids, hosted] = await Promise.allSettled([
      api.get<Identity[]>(`/identities?radio=${enc(id)}`),
      api.get<HostedSettings>('/hosted'),
    ])
    identities.value = ids.status === 'fulfilled' ? ids.value.filter((i) => (i.radio_id ?? 'main') === id || !i.radio_id) : []
    instances.value = hosted.status === 'fulfilled' ? hosted.value.instances.filter((x) => x.radio === id) : []
    loaded.value = true
  },
  { immediate: true },
)

const byNode = computed(() => new Map(instances.value.filter((x) => x.node_id).map((x) => [x.node_id!, x])))
const relay = computed(() => identities.value.find((i) => i.is_relay))
const others = computed(() => identities.value.filter((i) => !i.is_relay))
const relayInstance = computed(() => instances.value.find((x) => x.role === 'persona'))

function runsOn(i: Identity) {
  const x = byNode.value.get(i.node_id)
  if (!i.real_node) return { text: i.multi_radio ? 'RepeaterTastic (routed across radios)' : 'RepeaterTastic', state: '' }
  if (!x) return { text: 'meshtasticd', state: 'starting' }
  return { text: `meshtasticd ${x.firmware || ''}`.trim(), state: x.connected ? 'running' : x.running ? 'starting' : 'stopped' }
}
</script>

<template>
  <section class="mt-5 space-y-4">
    <div>
      <div class="flex items-center justify-between gap-2">
        <span class="label !mb-0">Relay</span>
        <span v-if="relayInstance" :class="['chip', relayInstance.connected ? 'bg-ok/14 text-ok' : 'bg-warn/15 text-warn']">{{ relayInstance.connected ? 'running' : 'starting' }}</span>
      </div>
      <div v-if="relay" class="mt-1.5 rounded-xl border border-line-soft px-3.5 py-2.5 text-[13px]">
        <div class="flex flex-wrap items-baseline gap-x-2">
          <span class="font-medium">{{ relay.long_name }}</span>
          <span class="mono text-xs text-ink-3">{{ relay.node_id }}</span>
        </div>
        <p v-if="relayInstance" class="mt-1 text-xs text-ink-3">
          Runs on meshtasticd {{ relayInstance.firmware || '' }} ({{ relayInstance.launcher }}, port {{ relayInstance.port }}<template v-if="relayInstance.restarts">, {{ relayInstance.restarts }} restarts</template>).
          RepeaterTastic gives it its saved key and sets its region, preset and role from this page.
          <span v-if="relayInstance.last_error" class="text-warn">Last stop: {{ relayInstance.last_error }}</span>
        </p>
        <p v-else class="mt-1 text-xs text-ink-3">Runs inside RepeaterTastic. Configuration → Experimental can move it to meshtasticd.</p>
      </div>
      <div v-else-if="!loaded" class="mt-1.5 h-14 animate-pulse rounded-xl bg-sunken" />
    </div>

    <div>
      <div class="flex items-center justify-between gap-2">
        <span class="label !mb-0">Identities on this radio</span>
        <span class="chip bg-brand/12 text-brand">0 hops from the air</span>
      </div>
      <div class="scroll-thin mt-1.5 overflow-x-auto rounded-xl border border-line-soft">
        <table class="tbl text-xs">
          <thead><tr><th>Identity</th><th>Runs on</th><th>App port</th><th class="num">Hops</th></tr></thead>
          <tbody>
            <tr v-for="i in others" :key="i.node_id">
              <td class="font-medium">{{ i.long_name }}</td>
              <td>
                {{ runsOn(i).text }}
                <span v-if="runsOn(i).state" :class="['chip ml-1', runsOn(i).state === 'running' ? 'bg-ok/14 text-ok' : 'bg-warn/15 text-warn']">{{ runsOn(i).state }}</span>
              </td>
              <td class="mono">{{ i.api ? `:${i.api.port}` : '—' }}</td>
              <td class="num"><span class="chip bg-brand/12 text-brand">0</span></td>
            </tr>
            <tr v-if="loaded && !others.length"><td colspan="4" class="text-ink-3">No identities yet.</td></tr>
          </tbody>
        </table>
      </div>
      <p class="hint">Each identity transmits on this radio itself, beside the relay. The relay hears them but never repeats them: they already went out from this mast.</p>
    </div>
  </section>
</template>
