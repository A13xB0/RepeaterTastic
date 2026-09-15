<script setup lang="ts">
// The one place a channel slot is set: add or edit a slot on one identity, or add a channel to
// several identities (each gets its first free slot). With identities on several radios switched
// on, each slot is on exactly one radio; Scotland on LongFast and on MediumFast are two slots.
import { computed, ref, watch } from 'vue'
import { Dices } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Channel, Identity } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Toggle from '@/components/ui/Toggle.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, multiRadioActive, radioChoices, radioName, upsertIdentity } from '@/store/live'
import { toast, toastError } from '@/composables/toast'
import { channelSlots, freeSlot } from '@/lib/channels'
import { num } from '@/lib/format'

/** A channel as it exists on the site: same name, key and radio. */
export interface SiteChannel {
  key: string
  name: string
  psk: string
  radio?: string
  holders: string[]
  /** A radio's primary channel offered as a ready-made channel. */
  primary?: boolean
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

const findIdentity = (id?: string) => live.identities.find((i) => i.node_id === id) ?? live.allIdentities.find((i) => i.node_id === id)
const identity = computed(() => findIdentity(props.identityId))
const single = computed(() => props.slot !== undefined && !!identity.value)
const existing = computed<Channel | undefined>(() => (single.value ? channelSlots(identity.value!)[props.slot!] : undefined))
const editing = computed(() => !!existing.value && existing.value.role !== 'DISABLED')

// Radios are only chosen with the experimental switch on and more than one radio; never for relays.
const multi = computed(() => multiRadioActive())
const choices = computed(() => radioChoices())
const showRadio = computed(() => multi.value && !(single.value && identity.value?.is_relay))
/** An identity's default radio: where its slot 0 lives. */
const defaultRadioOf = (i?: Identity) => (i ? (channelSlots(i)[0]?.radio ?? i.radio_id ?? 'main') : 'main')

const mode = ref<'existing' | 'new'>('existing')
const chosen = ref('')
const name = ref('')
const keyKind = ref<'keep' | 'random' | 'default' | 'custom' | 'none'>('random')
const customPsk = ref('')
const uplink = ref(false)
const downlink = ref(false)
const radio = ref('')
const bulkRadio = ref<'default' | 'one'>('default')
const targets = ref<string[]>([])
const saving = ref(false)
const error = ref('')

const offered = computed<SiteChannel[]>(() => {
  const list = [...props.channels]
  if (multi.value)
    for (const r of live.radios) { // ready-made primaries need a running radio's PHY
      const n = r.phy.primary_channel || r.phy.preset_name
      if (!list.some((c) => c.name === n && c.psk === 'AQ==' && c.radio === r.id))
        list.push({ key: `primary|${r.id}`, name: n, psk: 'AQ==', radio: r.id, holders: [], primary: true })
    }
  return list
})
const label = (c: SiteChannel) =>
  c.primary ? `${radioName(c.radio!)} primary (${c.name}, default key)` : `${c.name}${multi.value && c.radio ? ` · ${radioName(c.radio)}` : ''} · on ${c.holders.length} identit${c.holders.length === 1 ? 'y' : 'ies'}`

watch(
  () => props.open,
  (o) => {
    if (!o) return
    error.value = ''
    targets.value = []
    bulkRadio.value = 'default'
    customPsk.value = ''
    const ex = editing.value ? existing.value! : undefined
    if (ex) {
      mode.value = 'new'
      name.value = ex.name
      keyKind.value = 'keep'
      uplink.value = ex.uplink
      downlink.value = ex.downlink
      radio.value = ex.radio ?? defaultRadioOf(identity.value)
    } else {
      mode.value = offered.value.length ? 'existing' : 'new'
      chosen.value = props.preset?.key ?? offered.value[0]?.key ?? ''
      name.value = ''
      keyKind.value = 'random'
      uplink.value = downlink.value = false
      radio.value = props.preset?.radio ?? defaultRadioOf(identity.value)
    }
  },
)
// Picking an existing channel picks its radio too (it can still be changed).
watch(chosen, (k) => {
  const c = offered.value.find((x) => x.key === k)
  if (c?.radio && mode.value === 'existing') radio.value = c.radio
  if (c?.radio && !single.value) bulkRadio.value = 'one'
})

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

const has = (i: Identity, c: { name: string; psk: string }, r?: string) =>
  channelSlots(i).some((x) => x.role === 'SECONDARY' && x.name === c.name && x.psk === c.psk && (!multi.value || !r || (x.radio ?? defaultRadioOf(i)) === r))
// Bulk candidates: this radio's identities, plus identities from other radios that are on this one.
const candidates = computed(() => {
  const seen = new Set<string>()
  return [...live.identities, ...(multi.value ? live.allIdentities : [])].filter((i) => {
    if (seen.has(i.node_id)) return false
    seen.add(i.node_id)
    return !(channel.value && has(i, channel.value, bulkRadio.value === 'one' ? radio.value : undefined))
  })
})

// Why the radio can't be chosen, when it can't.
const radioWhy = computed(() => {
  if (single.value && identity.value?.is_relay) return 'A relay persona keeps its channels on its own radio.'
  if (choices.value.length < 2) return 'Add a second radio under Configuration → Radios to choose another.'
  if (!live.multiRadioIdentities) return 'Turn on Configuration → Experimental → Identities on several radios to choose another.'
  return ''
})
const fixedRadio = computed(() => {
  const id = single.value ? (existing.value?.radio ?? identity.value?.radio_id ?? 'main') : (live.radios[0]?.id ?? 'main')
  const r = live.radios.find((x) => x.id === id)
  return r ? `${r.name} · ${r.phy.preset_name} · ${num(r.phy.frequency_mhz, 3)} MHz` : radioName(id)
})

const airtimeNote = computed(() => {
  const r = live.radios.find((x) => x.id === radio.value)
  return r ? `${num(r.phy.frequency_mhz, 3)} MHz · SF${r.phy.sf}` : ''
})

function randomPsk(): string {
  const b = new Uint8Array(32)
  crypto.getRandomValues(b)
  return btoa(String.fromCharCode(...b))
}

async function save() {
  const c = channel.value
  if (!c) return
  // a random key is made once, so every identity in this batch shares it
  const psk = mode.value === 'new' && keyKind.value === 'random' ? randomPsk() : c.psk
  const plan: { id: Identity; slot: number; radio?: string }[] = []
  const skipped: string[] = []
  if (single.value) {
    plan.push({ id: identity.value!, slot: props.slot!, radio: showRadio.value && props.slot! > 0 ? radio.value : undefined })
  } else {
    for (const nodeId of targets.value) {
      const i = findIdentity(nodeId)
      const slot = i && freeSlot(i)
      if (!i) continue
      if (slot === undefined) {
        skipped.push(i.long_name)
        continue
      }
      const r = !multi.value || i.is_relay ? undefined : bulkRadio.value === 'one' ? radio.value : defaultRadioOf(i)
      plan.push({ id: i, slot, radio: r })
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
      const body: Record<string, unknown> = { name: c.name, psk, role: 'SECONDARY', uplink: uplink.value, downlink: downlink.value }
      if (p.radio) body.radio = p.radio
      upsertIdentity(await api.put<Identity>(`/identities/${enc(p.id.node_id)}/channels/${p.slot}`, body))
    }
    const where = plan.length === 1 ? plan[0]!.id.long_name : `${plan.length} identities`
    toast(`${c.name} ${editing.value ? 'saved on' : 'added to'} ${where}${skipped.length ? ` · no free slot on ${skipped.join(', ')}` : ''}`)
    emit('close')
  } catch (e) {
    toastError(e)
  } finally {
    saving.value = false
  }
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
      <div v-if="!editing" class="seg" role="group" aria-label="Channel source">
        <button type="button" :aria-pressed="mode === 'existing'" :disabled="!offered.length" @click="mode = 'existing'">Existing channel</button>
        <button type="button" :aria-pressed="mode === 'new'" @click="mode = 'new'">New channel</button>
      </div>

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

      <!-- one radio for this slot -->
      <div v-if="showRadio && single && slot! > 0" class="grid gap-2 rounded-xl border border-line-soft bg-raised px-3.5 py-3">
        <div class="flex flex-wrap items-baseline justify-between gap-2">
          <span class="text-[13px] font-medium">Radio</span>
          <span class="text-xs text-ink-3">the one radio this slot hears and sends on</span>
        </div>
        <label v-for="r in choices" :key="r.id" class="flex flex-wrap items-center gap-2 text-[13px]">
          <input v-model="radio" type="radio" :value="r.id" class="accent-[var(--brand)]" />
          {{ r.name }}
          <span v-if="r.id === defaultRadioOf(identity)" class="chip bg-ink-3/12 text-ink-3">default</span>
          <span :class="['text-xs', r.pending ? 'text-warn' : 'text-ink-3']">{{ r.detail }}</span>
        </label>
        <p v-if="choices.find((r) => r.id === radio)?.pending" class="hint !mt-0 !text-warn">That radio starts at the next restart; until then this channel runs on the default radio.</p>
        <p class="hint !mt-0">Want it on another radio too? Add the same channel to another slot and choose that radio.</p>
      </div>
      <p v-else-if="showRadio && single && slot === 0" class="hint">Slot 0 is the primary channel of {{ identity?.long_name }}'s default radio. Change the default radio in the identity editor.</p>
      <div v-else-if="single" class="grid gap-1 rounded-xl border border-line-soft bg-raised px-3.5 py-3">
        <span class="text-[13px] font-medium">Radio</span>
        <span class="text-[13px]">{{ fixedRadio }}</span>
        <p class="hint !mt-0">{{ radioWhy }}</p>
      </div>

      <fieldset v-if="!single">
        <legend class="label">Identities</legend>
        <div class="grid gap-1 sm:grid-cols-2">
          <label v-for="i in candidates" :key="i.node_id" class="flex items-center gap-2 text-[13px]">
            <input v-model="targets" type="checkbox" :value="i.node_id" class="size-4 accent-[var(--brand)]" :disabled="freeSlot(i) === undefined" />
            <span :class="freeSlot(i) === undefined ? 'text-ink-3' : ''">{{ i.long_name }}{{ i.is_relay ? ' (relay)' : '' }}{{ multi && i.radio_name ? ` · ${i.radio_name}` : '' }}{{ freeSlot(i) === undefined ? ' · full' : '' }}</span>
          </label>
        </div>
        <p v-if="!candidates.length" class="hint">Every identity already has this channel.</p>
      </fieldset>

      <div v-if="!showRadio && !single" class="grid gap-1 rounded-xl border border-line-soft bg-raised px-3.5 py-3">
        <span class="text-[13px] font-medium">Radio</span>
        <span class="text-[13px]">Each identity's own radio</span>
        <p class="hint !mt-0">{{ radioWhy }}</p>
      </div>
      <div v-if="showRadio && !single" class="grid gap-2 rounded-xl border border-line-soft bg-raised px-3.5 py-3 text-[13px]">
        <span class="font-medium">Radio</span>
        <label class="flex items-center gap-2"><input v-model="bulkRadio" type="radio" value="default" class="accent-[var(--brand)]" /> Each identity's default radio</label>
        <label class="flex flex-wrap items-center gap-2">
          <input v-model="bulkRadio" type="radio" value="one" class="accent-[var(--brand)]" /> One radio for all:
          <select v-model="radio" class="input !h-8 !w-auto !py-0 text-xs" aria-label="Radio for all" @focus="bulkRadio = 'one'">
            <option v-for="r in choices" :key="r.id" :value="r.id">{{ r.name }}{{ r.pending ? ' (starts at restart)' : '' }}</option>
          </select>
          <span v-if="bulkRadio === 'one'" class="text-xs text-ink-3">{{ airtimeNote }}</span>
        </label>
        <p class="hint !mt-0">Relay personas always keep their channels on their own radio.</p>
      </div>

      <div class="flex flex-wrap gap-5 text-[13px]">
        <label class="flex items-center gap-2"><Toggle v-model="uplink" label="MQTT uplink" />MQTT uplink</label>
        <label class="flex items-center gap-2"><Toggle v-model="downlink" label="MQTT downlink" />MQTT downlink</label>
      </div>
      <p v-if="error" class="hint !text-bad">{{ error }}</p>
    </div>
    <template #footer>
      <button class="btn" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" :disabled="saving || !channel || (!single && !targets.length)" @click="save">
        <Spinner v-if="saving" />{{ single ? (editing ? 'Save channel' : 'Add channel') : `Add to ${targets.length || ''} identit${targets.length === 1 ? 'y' : 'ies'}` }}
      </button>
    </template>
  </Modal>
</template>
