<script setup lang="ts">
// Experimental: which radios an identity is on, which radio each channel listens and sends on,
// how DMs pick a radio, and a preview of the choice. Set here only, never from the app.
import { computed, ref, watch } from 'vue'
import { FlaskConical } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Identity, MultiRadio } from '@/api/types'
import { live } from '@/store/live'

const props = defineProps<{ identity: Identity }>()
const model = defineModel<MultiRadio | null>({ required: true })

const home = computed(() => props.identity.radio_id ?? 'main')
const radioName = (id: string) => live.radios.find((r) => r.id === id)?.name ?? id
const others = computed(() => live.radios.filter((r) => r.id !== home.value))
const attached = computed(() => [home.value, ...(model.value?.radios ?? []).filter((r) => r !== home.value && live.radios.some((x) => x.id === r))])
const channels = computed(() => props.identity.channels.filter((c) => c.role !== 'DISABLED'))

function ensure(): MultiRadio {
  if (!model.value) model.value = { radios: [], dm: 'auto', fallback: false }
  return model.value
}
function toggleRadio(id: string, on: boolean) {
  const m = ensure()
  m.radios = on ? [...new Set([...m.radios, id])] : m.radios.filter((r) => r !== id)
  // drop routes that point at a radio the identity just left
  for (const [k, v] of Object.entries(m.listen ?? {})) m.listen![k] = v.filter((r) => r !== id || on)
  for (const [k, v] of Object.entries(m.send ?? {})) if (!on && v === id) delete m.send![k]
  if (!on && m.dm === id) m.dm = 'auto'
  if (!m.radios.length && !Object.keys(m.send ?? {}).length) model.value = { ...m }
}

// Defaults mirror the daemon: the primary listens on every attached radio, others on home only.
function listens(index: number, radio: string): boolean {
  const l = model.value?.listen?.[String(index)]
  if (l) return l.includes(radio)
  return index === 0 || radio === home.value
}
function setListen(index: number, radio: string, on: boolean) {
  const m = ensure()
  m.listen ??= {}
  const cur = attached.value.filter((r) => listens(index, r))
  m.listen[String(index)] = on ? [...new Set([...cur, radio])] : cur.filter((r) => r !== radio)
}
function sendOf(index: number): string {
  return model.value?.send?.[String(index)] || home.value
}
function setSend(index: number, value: string) {
  const m = ensure()
  m.send ??= {}
  if (value === home.value) delete m.send[String(index)]
  else m.send[String(index)] = value
}
const dm = computed({
  get: () => model.value?.dm || 'auto',
  set: (v: string) => (ensure().dm = v),
})
const fallback = computed({
  get: () => !!model.value?.fallback,
  set: (v: boolean) => (ensure().fallback = v),
})

// ---- preview (of the saved routing)
const previewTo = ref('')
const previewChannel = ref(0)
const preview = ref<{ radio_names: string[]; reason: string; enabled: boolean } | null>(null)
const previewBusy = ref(false)
const nodes = computed(() => Object.values(live.nodes).filter((n) => !n.local).sort((a, b) => (b.last_heard ?? 0) - (a.last_heard ?? 0)).slice(0, 200))
async function runPreview() {
  previewBusy.value = true
  try {
    const q = previewTo.value ? `to=${enc(previewTo.value)}` : `channel=${previewChannel.value}`
    preview.value = await api.get(`/identities/${enc(props.identity.node_id)}/route?${q}`)
  } catch {
    preview.value = null
  } finally {
    previewBusy.value = false
  }
}
watch(() => props.identity.node_id, () => (preview.value = null))
</script>

<template>
  <div class="rounded-xl border border-dashed border-line px-3.5 py-3">
    <div class="flex items-center gap-2 text-[13px] font-medium"><FlaskConical class="size-4 text-ink-3" />Radios <span class="chip bg-ink-3/12 text-ink-3">experimental</span></div>
    <p class="hint !mt-1">Home: {{ radioName(home) }}. Extra radios hear and send for this identity as the table below says. Only set here; the Meshtastic app can't change it.</p>

    <fieldset class="mt-3">
      <legend class="label">Also on</legend>
      <div class="flex flex-wrap gap-x-4 gap-y-1">
        <label v-for="r in others" :key="r.id" class="flex items-center gap-2 text-[13px]">
          <input type="checkbox" class="size-4 accent-[var(--brand)]" :checked="model?.radios?.includes(r.id)" @change="toggleRadio(r.id, ($event.target as HTMLInputElement).checked)" />
          {{ r.name }} <span class="text-xs text-ink-3">{{ r.phy.preset_name }}</span>
        </label>
      </div>
    </fieldset>

    <template v-if="attached.length > 1">
      <div class="mt-3 overflow-x-auto">
        <table class="tbl text-xs">
          <thead>
            <tr>
              <th>Channel</th>
              <th v-for="r in attached" :key="r" class="text-center">Listen on {{ radioName(r) }}</th>
              <th>Send on</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in channels" :key="c.index">
              <td class="whitespace-nowrap">
                <span class="font-medium">{{ c.index === 0 ? 'Primary' : c.display_name }}</span>
                <span v-if="c.index === 0" class="block text-2xs text-ink-3">named after each radio's preset</span>
              </td>
              <td v-for="r in attached" :key="r" class="text-center">
                <input type="checkbox" class="size-4 accent-[var(--brand)]" :aria-label="`Channel ${c.display_name} listens on ${radioName(r)}`" :checked="listens(c.index, r)" @change="setListen(c.index, r, ($event.target as HTMLInputElement).checked)" />
              </td>
              <td>
                <select class="input !h-8 !py-0 text-xs" :aria-label="`Channel ${c.display_name} sends on`" :value="sendOf(c.index)" @change="setSend(c.index, ($event.target as HTMLSelectElement).value)">
                  <option v-for="r in attached" :key="r" :value="r">{{ radioName(r) }}{{ r === home ? ' (home)' : '' }}</option>
                  <option value="all">All radios</option>
                </select>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="hint">“All radios” sends every message once per radio, using each radio's airtime. Keep it for channels that really need it.</p>

      <div class="mt-3 grid gap-3 sm:grid-cols-2">
        <div>
          <label class="label" for="mr-dm">Direct messages</label>
          <select id="mr-dm" v-model="dm" class="input">
            <option value="auto">Radio where they were last heard best</option>
            <option value="home">Always the home radio</option>
            <option v-for="r in attached.filter((x) => x !== home)" :key="r" :value="r">Always {{ radioName(r) }}</option>
          </select>
        </div>
        <label class="flex items-start gap-2 self-end text-[13px]">
          <input v-model="fallback" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
          <span>Retry a failed DM on another radio<span class="block text-xs text-ink-3">Once, only if they were heard there in the last day and that radio has airtime left.</span></span>
        </label>
      </div>

      <div class="mt-3 rounded-lg bg-raised px-3 py-2.5">
        <div class="text-xs font-medium">Where would it go? <span class="font-normal text-ink-3">(uses the saved routing)</span></div>
        <div class="mt-2 flex flex-wrap items-end gap-2">
          <div>
            <label class="label" for="mr-to">To</label>
            <input id="mr-to" v-model.trim="previewTo" class="input mono !h-8 w-40" list="mr-nodes" placeholder="!node or empty" />
            <datalist id="mr-nodes"><option v-for="n in nodes" :key="n.node_id" :value="n.node_id">{{ n.long_name }}</option></datalist>
          </div>
          <div v-if="!previewTo">
            <label class="label" for="mr-ch">Channel</label>
            <select id="mr-ch" v-model.number="previewChannel" class="input !h-8 !py-0">
              <option v-for="c in channels" :key="c.index" :value="c.index">{{ c.index === 0 ? 'Primary' : c.display_name }}</option>
            </select>
          </div>
          <button type="button" class="btn btn-sm" :disabled="previewBusy" @click="runPreview">Check</button>
        </div>
        <p v-if="preview" class="mt-2 text-xs">
          <span class="font-medium">{{ preview.radio_names.join(' + ') }}</span> · {{ preview.reason }}
          <span v-if="!preview.enabled" class="text-warn"> · the experimental switch is off, so everything uses the home radio</span>
        </p>
      </div>
    </template>
  </div>
</template>
