<script setup lang="ts">
// The one place a channel slot is set: add or edit a slot on one identity, or add a channel to
// several identities (each gets its first free slot). Scotland on LongFast and on MediumFast are
// two different channels.
import { computed, ref, watch } from 'vue'
import { Dices } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Channel, Identity } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Toggle from '@/components/ui/Toggle.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, upsertIdentity } from '@/store/live'
import { toast, toastError } from '@/composables/toast'
import { channelSlots, freeSlot } from '@/lib/channels'

/** A channel as it exists on the site: same name and key. */
export interface SiteChannel {
  key: string
  name: string
  psk: string
  holders: string[]
}

const props = defineProps<{
  open: boolean
  /** One identity and slot; omit both for "add to several identities". */
  identityId?: string
  slot?: number
  /** Start with this existing channel selected (bulk add from the channel list). */
  preset?: SiteChannel | null
  channels: SiteChannel[]
}>()
const emit = defineEmits<{ close: [] }>()

const findIdentity = (id?: string) => live.identities.find((i) => i.node_id === id)
const identity = computed(() => findIdentity(props.identityId))
const single = computed(() => props.slot !== undefined && !!identity.value)
const existing = computed<Channel | undefined>(() => (single.value ? channelSlots(identity.value!)[props.slot!] : undefined))
const editing = computed(() => !!existing.value && existing.value.role !== 'DISABLED')

const mode = ref<'existing' | 'new'>('existing')
const chosen = ref('')
const name = ref('')
const keyKind = ref<'keep' | 'random' | 'default' | 'custom' | 'none'>('random')
const customPsk = ref('')
const uplink = ref(false)
const downlink = ref(false)
const targets = ref<string[]>([])
const saving = ref(false)
const error = ref('')

const offered = computed<SiteChannel[]>(() => props.channels)
const label = (c: SiteChannel) => `${c.name} · on ${c.holders.length} identit${c.holders.length === 1 ? 'y' : 'ies'}`

watch(
  () => props.open,
  (o) => {
    if (!o) return
    error.value = ''
    targets.value = []
    customPsk.value = ''
    const ex = editing.value ? existing.value! : undefined
    if (ex) {
      mode.value = 'new'
      name.value = ex.name
      keyKind.value = 'keep'
      uplink.value = ex.uplink
      downlink.value = ex.downlink
    } else {
      mode.value = offered.value.length ? 'existing' : 'new'
      chosen.value = props.preset?.key ?? offered.value[0]?.key ?? ''
      name.value = ''
      keyKind.value = 'random'
      uplink.value = downlink.value = false
    }
  },
)

const selected = computed(() => offered.value.find((c) => c.key === chosen.value))
const channel = computed<{ name: string; psk: string } | null>(() => {
  if (mode.value === 'existing') return selected.value ? { name: selected.value.name, psk: selected.value.psk } : null
  const n = name.value.trim()
  if (!n || new TextEncoder().encode(n).length > 11) return null
  switch (keyKind.value) {
    case 'keep':
      return { name: n, psk: existing.value?.psk ?? '' }
    case 'default':
      return { name: n, psk: 'AQ==' }
    case 'none':
    case 'random':
      return { name: n, psk: '' }
    case 'custom':
      try {
        return [1, 16, 32].includes(atob(customPsk.value.trim()).length) ? { name: n, psk: customPsk.value.trim() } : null
      } catch {
        return null
      }
  }
  return null
})

const has = (i: Identity, c: { name: string; psk: string }) => channelSlots(i).some((x) => x.role === 'SECONDARY' && x.name === c.name && x.psk === c.psk)
const candidates = computed(() => live.identities.filter((i) => !(channel.value && has(i, channel.value))))

function randomPsk(): string {
  const b = new Uint8Array(32)
  crypto.getRandomValues(b)
  return btoa(String.fromCodePoint(...b))
}

/** Which identity/slot pairs get this channel: the given slot alone, or each target's first free slot. */
function buildPlan(): { plan: { id: Identity; slot: number }[]; skipped: string[] } {
  if (single.value) return { plan: [{ id: identity.value!, slot: props.slot! }], skipped: [] }
  const plan: { id: Identity; slot: number }[] = []
  const skipped: string[] = []
  for (const nodeId of targets.value) {
    const i = findIdentity(nodeId)
    if (!i) continue
    const slot = freeSlot(i)
    if (slot === undefined) {
      skipped.push(i.long_name)
      continue
    }
    plan.push({ id: i, slot })
  }
  return { plan, skipped }
}

/** PUTs the channel onto every planned slot, collecting per-identity failures. */
async function applyPlan(plan: { id: Identity; slot: number }[], name: string, psk: string): Promise<{ done: number; failed: string[] }> {
  const failed: string[] = []
  let done = 0
  for (const p of plan) {
    const body = { name, psk, role: 'SECONDARY', uplink: uplink.value, downlink: downlink.value }
    try {
      upsertIdentity(await api.put<Identity>(`/identities/${enc(p.id.node_id)}/channels/${p.slot}`, body))
      done++
    } catch (e) {
      failed.push(`${p.id.long_name}: ${(e as Error).message}`)
    }
  }
  return { done, failed }
}

function successMessage(c: { name: string }, plan: { id: Identity; slot: number }[], skipped: string[]): string {
  const where = plan.length === 1 ? plan[0]!.id.long_name : `${plan.length} identities`
  const verb = editing.value ? 'saved on' : 'added to'
  const skipNote = skipped.length ? ` · no free slot on ${skipped.join(', ')}` : ''
  return `${c.name} ${verb} ${where}${skipNote}`
}

async function save() {
  const c = channel.value
  if (!c) return
  // a random key is made once, so every identity in this batch shares it
  const psk = mode.value === 'new' && keyKind.value === 'random' ? randomPsk() : c.psk
  const { plan, skipped } = buildPlan()
  if (!plan.length) {
    error.value = skipped.length ? `No free slot on ${skipped.join(', ')}.` : 'Pick at least one identity.'
    return
  }
  saving.value = true
  error.value = ''
  const { done, failed } = await applyPlan(plan, c.name, psk)
  saving.value = false
  if (failed.length) {
    const prefix = done ? `Saved on ${done}; ` : ''
    error.value = `${prefix}not saved on ${failed.join('; ')}`
    if (!done) toastError(new Error(failed[0]))
    return
  }
  toast(successMessage(c, plan, skipped))
  emit('close')
}
</script>

<template>
  <Modal
    :open="open"
    :title="single ? `${identity?.long_name ?? ''} · slot ${slot}` : 'Add a channel to identities'"
    :subtitle="single ? (editing ? 'Edit channel' : 'Add a channel') : 'Each identity gets its first free slot'"
    @close="emit('close')"
  >
    <div class="grid gap-4">
      <fieldset v-if="!editing" class="seg">
        <legend class="sr-only">Channel source</legend>
        <button type="button" :aria-pressed="mode === 'existing'" :disabled="!offered.length" @click="mode = 'existing'">Existing channel</button>
        <button type="button" :aria-pressed="mode === 'new'" @click="mode = 'new'">New channel</button>
      </fieldset>

      <div v-if="mode === 'existing' && !editing">
        <label class="label" for="cs-ch">Channel</label>
        <select id="cs-ch" v-model="chosen" class="input">
          <option v-for="c in offered" :key="c.key" :value="c.key">{{ label(c) }}</option>
        </select>
        <p class="hint">Same name and key, so they hear each other on it.</p>
      </div>

      <template v-else>
        <div>
          <label class="label" for="cs-name">Name</label>
          <input id="cs-name" v-model="name" class="input" maxlength="11" placeholder="e.g. Scotland" />
          <p v-if="editing" class="hint">Changing the name or key makes it a different channel: anyone still on the old one stops hearing this identity.</p>
        </div>
        <div>
          <span class="label">Key</span>
          <div class="grid gap-1.5 text-[13px]">
            <label v-if="editing" class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="keep" class="accent-[var(--brand)]" /> Keep the current key</label>
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="random" class="accent-[var(--brand)]" /> Random private key (AES-256) <Dices class="size-3.5 text-ink-3" /></label>
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="default" class="accent-[var(--brand)]" /> Meshtastic default key (anyone can read it)</label>
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="custom" class="accent-[var(--brand)]" /> Paste a key</label>
            <label class="flex items-center gap-2"><input v-model="keyKind" type="radio" value="none" class="accent-[var(--brand)]" /> No encryption</label>
          </div>
          <input v-if="keyKind === 'custom'" v-model="customPsk" class="input mono mt-2" spellcheck="false" placeholder="base64, e.g. from another node" aria-label="Pre-shared key" />
        </div>
      </template>

      <p v-if="single && slot === 0" class="hint">Slot 0 is the primary channel of {{ identity?.long_name }}'s radio.</p>

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
        <span class="flex items-center gap-2" @click.self="uplink = !uplink"><Toggle v-model="uplink" label="MQTT uplink" />MQTT uplink</span>
        <span class="flex items-center gap-2" @click.self="downlink = !downlink"><Toggle v-model="downlink" label="MQTT downlink" />MQTT downlink</span>
      </div>
      <p v-if="error" class="hint !text-bad">{{ error }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn" @click="emit('close')">Cancel</button>
      <button type="button" class="btn btn-primary" :disabled="saving || !channel || (!single && !targets.length)" @click="save">
        <Spinner v-if="saving" />{{ single ? (editing ? 'Save channel' : 'Add channel') : `Add to ${targets.length || ''} identit${targets.length === 1 ? 'y' : 'ies'}` }}
      </button>
    </template>
  </Modal>
</template>
