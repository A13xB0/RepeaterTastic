<script setup lang="ts">
// Experimental: an identity's default radio and how its DMs pick a radio. Which radio each channel
// is on is set per slot on the Channels page; this section only shows the result.
import { computed, ref, watch } from 'vue'
import { FlaskConical } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Identity, MultiRadio } from '@/api/types'
import { live, radioChoices, radioName } from '@/store/live'
import { channelSlots } from '@/lib/channels'

const props = defineProps<{ identity: Identity }>()
const model = defineModel<MultiRadio | null>({ required: true })

const home = computed(() => props.identity.radio_id ?? 'main')
function ensure(): MultiRadio {
  if (!model.value) model.value = {}
  return model.value
}
const dm = computed({
  get: () => model.value?.dm || 'auto',
  set: (v: string) => (ensure().dm = v),
})
const dmRadio = ref('')
watch(
  () => model.value?.dm,
  (v) => {
    if (v && v !== 'auto' && v !== 'default') dmRadio.value = v
  },
  { immediate: true },
)
const dmKind = computed({
  get: () => (dm.value === 'auto' || dm.value === 'default' ? dm.value : 'fixed'),
  set: (k: string) => (dm.value = k === 'fixed' ? dmRadio.value || radioChoices().find((r) => r.id !== (model.value?.default_radio || home.value))?.id || home.value : k),
})
const fallback = computed({
  get: () => !!model.value?.fallback,
  set: (v: boolean) => (ensure().fallback = v),
})

const slotsByRadio = computed(() => {
  const out = new Map<string, string[]>()
  for (const c of channelSlots(props.identity))
    if (c.role !== 'DISABLED') {
      const r = c.radio ?? home.value
      out.set(r, [...(out.get(r) ?? []), c.index === 0 ? `slot 0 (${c.display_name})` : c.display_name])
    }
  return out
})

// ---- preview (of the saved routing)
const previewTo = ref('')
const preview = ref<{ radio_names: string[]; reason: string; enabled: boolean } | null>(null)
const previewBusy = ref(false)
const nodes = computed(() => Object.values(live.nodes).filter((n) => !n.local).sort((a, b) => (b.last_heard ?? 0) - (a.last_heard ?? 0)).slice(0, 200))
async function runPreview() {
  if (!previewTo.value) return
  previewBusy.value = true
  try {
    preview.value = await api.get(`/identities/${enc(props.identity.node_id)}/route?to=${enc(previewTo.value)}`)
  } catch {
    preview.value = null
  } finally {
    previewBusy.value = false
  }
}
</script>

<template>
  <div class="grid gap-3 rounded-xl border border-dashed border-line px-3.5 py-3">
    <div class="flex items-center gap-2 text-[13px] font-medium"><FlaskConical class="size-4 text-ink-3" />Channels and DMs across radios <span class="chip bg-ink-3/12 text-ink-3">experimental</span></div>


    <dl class="kv !grid-cols-[auto_1fr] text-xs">
      <dt>Lives on</dt>
      <dd>{{ radioName(home) }}<template v-if="identity.api"> · app port {{ identity.api.port }}</template></dd>
      <dt>On radios</dt>
      <dd>
        <span v-for="[r, slots] in slotsByRadio" :key="r" class="mr-2 inline-block">{{ radioName(r) }} <span class="text-ink-3">({{ slots.join(', ') }})</span></span>
        <RouterLink to="/channels" class="text-brand hover:underline">Edit on Channels</RouterLink>
      </dd>
    </dl>

    <fieldset>
      <legend class="label">Direct messages</legend>
      <div class="grid gap-1.5 text-[13px]">
        <label class="flex items-center gap-2"><input v-model="dmKind" type="radio" value="auto" class="accent-[var(--brand)]" /> Radio where they were last heard best</label>
        <label class="flex items-center gap-2"><input v-model="dmKind" type="radio" value="default" class="accent-[var(--brand)]" /> Always the default radio</label>
        <label class="flex flex-wrap items-center gap-2">
          <input v-model="dmKind" type="radio" value="fixed" class="accent-[var(--brand)]" /> Always
          <select v-model="dmRadio" class="input !h-8 !w-auto !py-0 text-xs" aria-label="DM radio" :disabled="dmKind !== 'fixed'" @change="dm = dmRadio">
            <option v-for="r in radioChoices()" :key="r.id" :value="r.id">{{ r.name }}{{ r.pending ? ' (starts at restart)' : '' }}</option>
          </select>
        </label>
      </div>
    </fieldset>
    <label class="flex items-start gap-2 text-[13px]">
      <input v-model="fallback" type="checkbox" class="mt-0.5 size-4 accent-[var(--brand)]" />
      <span>Retry a failed DM once on another radio<span class="block text-xs text-ink-3">Only on a radio it's on where they were heard in the last day, with airtime left.</span></span>
    </label>

    <div class="rounded-lg bg-raised px-3 py-2.5">
      <div class="text-xs font-medium">Where would a DM go? <span class="font-normal text-ink-3">(saved settings)</span></div>
      <div class="mt-2 flex flex-wrap items-end gap-2">
        <input v-model.trim="previewTo" class="input mono !h-8 w-44" list="mr-nodes" placeholder="!node" aria-label="Destination node" />
        <datalist id="mr-nodes"><option v-for="n in nodes" :key="n.node_id" :value="n.node_id">{{ n.long_name }}</option></datalist>
        <button type="button" class="btn btn-sm" :disabled="previewBusy || !previewTo" @click="runPreview">Check</button>
      </div>
      <p v-if="preview" class="mt-2 text-xs">
        <span class="font-medium">{{ preview.radio_names.join(' + ') }}</span> · {{ preview.reason }}
      </p>
    </div>
  </div>
</template>
