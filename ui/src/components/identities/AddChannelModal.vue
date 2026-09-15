<script setup lang="ts">
// Add a channel to one identity's slot, or to several identities at once (each gets its first free
// slot). Pick a channel another identity already has, or make a new one.
import { computed, ref, watch } from 'vue'
import { Dices } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Identity } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Toggle from '@/components/ui/Toggle.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, upsertIdentity } from '@/store/live'
import { toast, toastError } from '@/composables/toast'
import { freeSlot } from '@/lib/channels'

export interface SiteChannel {
  key: string
  name: string
  psk: string
  holders: string[]
}

const props = defineProps<{
  open: boolean
  /** One identity and slot; omit for "add to several identities". */
  identityId?: string
  slot?: number
  /** Start with this existing channel selected. */
  preset?: SiteChannel | null
  channels: SiteChannel[]
}>()
const emit = defineEmits<{ close: [] }>()

const mode = ref<'existing' | 'new'>('existing')
const chosen = ref('')
const name = ref('')
const keyKind = ref<'random' | 'default' | 'custom' | 'none'>('random')
const customPsk = ref('')
const uplink = ref(false)
const downlink = ref(false)
const targets = ref<string[]>([])
const saving = ref(false)
const error = ref('')

const single = computed(() => !!props.identityId)
const identity = computed(() => live.identities.find((i) => i.node_id === props.identityId))

watch(
  () => props.open,
  (o) => {
    if (!o) return
    error.value = ''
    mode.value = props.channels.length ? 'existing' : 'new'
    chosen.value = props.preset?.key ?? props.channels[0]?.key ?? ''
    name.value = ''
    keyKind.value = 'random'
    customPsk.value = ''
    uplink.value = downlink.value = false
    targets.value = []
  },
)

function randomPsk(): string {
  const b = new Uint8Array(32)
  crypto.getRandomValues(b)
  return btoa(String.fromCharCode(...b))
}

const selected = computed(() => props.channels.find((c) => c.key === chosen.value))
const channel = computed<{ name: string; psk: string } | null>(() => {
  if (mode.value === 'existing') return selected.value ? { name: selected.value.name, psk: selected.value.psk } : null
  const n = name.value.trim()
  if (!n || new TextEncoder().encode(n).length > 11) return null
  const psk = keyKind.value === 'default' ? 'AQ==' : keyKind.value === 'none' ? '' : keyKind.value === 'custom' ? customPsk.value.trim() : ''
  if (keyKind.value === 'custom') {
    try {
      if (![1, 16, 32].includes(atob(psk).length)) return null
    } catch {
      return null
    }
  }
  return { name: n, psk }
})

const has = (i: Identity, c: { name: string; psk: string }) => i.channels.some((x) => x.role === 'SECONDARY' && x.name === c.name && x.psk === c.psk)
const candidates = computed(() => live.identities.filter((i) => !(channel.value && has(i, channel.value))))

async function save() {
  const c = channel.value
  if (!c) return
  // a random key is made once, so every identity in this batch shares it
  const psk = mode.value === 'new' && keyKind.value === 'random' ? randomPsk() : c.psk
  const plan: { id: Identity; slot: number }[] = []
  const skipped: string[] = []
  if (single.value && identity.value && props.slot !== undefined) {
    plan.push({ id: identity.value, slot: props.slot })
  } else {
    for (const nodeId of targets.value) {
      const i = live.identities.find((x) => x.node_id === nodeId)
      const slot = i && freeSlot(i)
      if (i && slot !== undefined) plan.push({ id: i, slot })
      else if (i) skipped.push(i.long_name)
    }
  }
  if (!plan.length) {
    error.value = skipped.length ? `No free slot on ${skipped.join(', ')}.` : 'Pick at least one identity.'
    return
  }
  saving.value = true
  error.value = ''
  try {
    for (const p of plan) {
      upsertIdentity(await api.put<Identity>(`/identities/${enc(p.id.node_id)}/channels/${p.slot}`, { name: c.name, psk, role: 'SECONDARY', uplink: uplink.value, downlink: downlink.value }))
    }
    toast(`${c.name} added to ${plan.length === 1 ? plan[0]!.id.long_name : `${plan.length} identities`}${skipped.length ? ` · no free slot on ${skipped.join(', ')}` : ''}`)
    emit('close')
  } catch (e) {
    toastError(e)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Modal :open="open" :title="single ? `Add a channel to ${identity?.long_name ?? ''}` : 'Add a channel to identities'" :subtitle="single ? `Slot ${slot}` : 'Each identity gets its first free slot'" @close="emit('close')">
    <div class="grid gap-4">
      <div class="seg" role="group" aria-label="Channel source">
        <button type="button" :aria-pressed="mode === 'existing'" :disabled="!channels.length" @click="mode = 'existing'">Existing channel</button>
        <button type="button" :aria-pressed="mode === 'new'" @click="mode = 'new'">New channel</button>
      </div>

      <div v-if="mode === 'existing'">
        <label class="label" for="ac-ch">Channel</label>
        <select id="ac-ch" v-model="chosen" class="input">
          <option v-for="c in channels" :key="c.key" :value="c.key">{{ c.name }} · on {{ c.holders.length }} identit{{ c.holders.length === 1 ? 'y' : 'ies' }}</option>
        </select>
        <p class="hint">Same name and key, so the identities hear each other on it.</p>
      </div>

      <template v-else>
        <div>
          <label class="label" for="ac-name">Name</label>
          <input id="ac-name" v-model="name" class="input" maxlength="11" placeholder="e.g. LothianOps" />
        </div>
        <div>
          <span class="label">Key</span>
          <div class="grid gap-1.5 text-[13px]">
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="random" class="accent-[var(--brand)]" /> Random private key (AES-256) <Dices class="size-3.5 text-ink-3" /></label>
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="default" class="accent-[var(--brand)]" /> Meshtastic default key (anyone can read it)</label>
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="custom" class="accent-[var(--brand)]" /> Paste a key</label>
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="none" class="accent-[var(--brand)]" /> No encryption</label>
          </div>
          <input v-if="keyKind === 'custom'" v-model="customPsk" class="input mono mt-2" spellcheck="false" placeholder="base64, e.g. from another node" aria-label="Pre-shared key" />
        </div>
      </template>

      <fieldset v-if="!single">
        <legend class="label">Identities</legend>
        <div class="grid gap-1 sm:grid-cols-2">
          <label v-for="i in candidates" :key="i.node_id" class="flex items-center gap-2 text-[13px]">
            <input v-model="targets" type="checkbox" :value="i.node_id" class="size-4 accent-[var(--brand)]" :disabled="freeSlot(i) === undefined" />
            <span :class="freeSlot(i) === undefined ? 'text-ink-3' : ''">{{ i.long_name }}{{ i.is_relay ? ' (relay)' : '' }}{{ freeSlot(i) === undefined ? ' · full' : '' }}</span>
          </label>
        </div>
        <p v-if="!candidates.length" class="hint">Every identity already has this channel.</p>
      </fieldset>

      <div class="flex flex-wrap gap-5 text-[13px]">
        <label class="flex items-center gap-2"><Toggle v-model="uplink" label="MQTT uplink" />MQTT uplink</label>
        <label class="flex items-center gap-2"><Toggle v-model="downlink" label="MQTT downlink" />MQTT downlink</label>
      </div>
      <p v-if="error" class="hint !text-bad">{{ error }}</p>
    </div>
    <template #footer>
      <button class="btn" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" :disabled="saving || !channel || (!single && !targets.length)" @click="save"><Spinner v-if="saving" />Add channel</button>
    </template>
  </Modal>
</template>
