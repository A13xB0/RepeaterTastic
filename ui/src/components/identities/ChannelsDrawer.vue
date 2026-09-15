<script setup lang="ts">
// One identity's channel slots, plus sharing and importing a channel URL. Adding and editing a
// slot opens the slot dialog, the same one the Channels page uses.
import { computed, ref, watch } from 'vue'
import { Lock, QrCode as QrIcon } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Channel, ChannelRole, Identity } from '@/api/types'
import Drawer from '@/components/ui/Drawer.vue'
import QrCode from '@/components/ui/QrCode.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import Spinner from '@/components/ui/Spinner.vue'
import ChannelSlotDialog, { type SiteChannel } from '@/components/identities/ChannelSlotDialog.vue'
import { multiRadioActive, live, radioName, upsertIdentity } from '@/store/live'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { channelSlots } from '@/lib/channels'

const props = defineProps<{ identityId: string | null; focus?: number }>()
const emit = defineEmits<{ close: [] }>()

const identity = computed<Identity | undefined>(() => live.identities.find((i) => i.node_id === props.identityId) ?? live.allIdentities.find((i) => i.node_id === props.identityId))
const multi = computed(() => multiRadioActive())
const tab = ref<'edit' | 'share'>('edit')
const slotDialog = ref<number | null>(null)
const shareUrl = ref('')
const importUrl = ref('')
const importing = ref(false)

watch(
  () => props.identityId,
  (id) => {
    tab.value = 'edit'
    shareUrl.value = ''
    importUrl.value = ''
    slotDialog.value = id && props.focus !== undefined && props.focus > 0 ? props.focus : null
  },
)

// Channels other identities have, offered as "existing channel" in the dialog.
const siteChannels = computed<SiteChannel[]>(() => {
  const map = new Map<string, SiteChannel>()
  for (const i of [...live.identities, ...(multi.value ? live.allIdentities : [])])
    for (const c of i.channels)
      if (c.role === 'SECONDARY') {
        const radio = multi.value ? (c.radio ?? i.radio_id) : undefined
        const key = `${c.name}|${c.psk}|${radio ?? ''}`
        if (!map.has(key)) map.set(key, { key, name: c.name, psk: c.psk, radio, holders: [] })
        if (!map.get(key)!.holders.includes(i.node_id)) map.get(key)!.holders.push(i.node_id)
      }
  return [...map.values()]
})

const pskKind = (psk: string) => {
  if (!psk) return 'no encryption'
  if (psk === 'AQ==') return 'default key'
  const len = atob(psk).length
  return len === 1 ? `default key #${atob(psk).charCodeAt(0)}` : `AES-${len * 8}`
}

async function remove(ch: Channel) {
  if (!identity.value) return
  const ok = await confirmDialog({
    title: `Remove ${ch.display_name} from ${identity.value.long_name}?`,
    body: `Slot ${ch.index} becomes free and ${identity.value.long_name} stops hearing and sending on it.`,
    confirm: 'Remove channel',
    danger: true,
  })
  if (!ok) return
  try {
    upsertIdentity(await api.put<Identity>(`/identities/${enc(identity.value.node_id)}/channels/${ch.index}`, { name: '', psk: '', role: 'DISABLED', uplink: false, downlink: false }))
    toast(`${ch.display_name} removed`)
  } catch (e) {
    toastError(e)
  }
}

async function loadShare() {
  if (!identity.value) return
  try {
    shareUrl.value = (await api.get<{ url: string }>(`/identities/${enc(identity.value.node_id)}/channels/url`)).url
  } catch (e) {
    toastError(e)
  }
}
watch(tab, (t) => t === 'share' && loadShare())

async function doImport() {
  if (!identity.value || !importUrl.value.trim()) return
  importing.value = true
  try {
    upsertIdentity(await api.post<Identity>(`/identities/${enc(identity.value.node_id)}/channels/url`, { url: importUrl.value.trim() }))
    toast('Channels imported')
    importUrl.value = ''
    tab.value = 'edit'
  } catch (e) {
    toastError(e)
  } finally {
    importing.value = false
  }
}

const roleCls = (r: ChannelRole) => (r === 'PRIMARY' ? 'bg-brand/14 text-brand' : r === 'SECONDARY' ? 'bg-info/12 text-info' : 'bg-ink-3/12 text-ink-3')
</script>

<template>
  <Drawer :open="!!identity" :title="`Channels · ${identity?.long_name ?? ''}`" :subtitle="identity?.node_id" wide @close="emit('close')">
    <div v-if="identity">
      <div class="tabs -mx-5 -mt-4 mb-4 px-5" role="tablist">
        <button role="tab" :aria-selected="tab === 'edit'" @click="tab = 'edit'">Slots</button>
        <button role="tab" :aria-selected="tab === 'share'" @click="tab = 'share'">Share &amp; import</button>
      </div>

      <div v-if="tab === 'edit'" class="space-y-2">
        <div v-for="ch in channelSlots(identity)" :key="ch.index" class="flex items-center gap-3 rounded-xl border border-line-soft bg-raised/60 px-3.5 py-2.5">
          <span class="flex size-7 shrink-0 items-center justify-center rounded-lg bg-sunken text-xs font-semibold tabular-nums text-ink-2">{{ ch.index }}</span>
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <span :class="['truncate text-[13px] font-medium', ch.role === 'DISABLED' && 'text-ink-3']">{{ ch.role === 'DISABLED' ? 'Unused' : ch.display_name }}</span>
              <span :class="['chip', roleCls(ch.role)]">{{ ch.role.toLowerCase() }}</span>
              <Lock v-if="ch.index === 0" class="size-3.5 text-ink-3" />
              <span v-if="multi && ch.role !== 'DISABLED'" :class="['chip', ch.radio_removed ? 'bg-bad/12 text-bad' : 'bg-ink-3/12 text-ink-2']">{{ ch.radio_removed ? 'radio removed' : ch.radio_pending ? `${radioName(ch.radio_pending)} after restart` : radioName(ch.radio ?? identity.radio_id ?? 'main') }}</span>
            </div>
            <div v-if="ch.role !== 'DISABLED'" class="mt-0.5 text-xs text-ink-3">
              {{ pskKind(ch.psk) }} · hash <span class="mono">0x{{ ch.hash.toString(16).padStart(2, '0') }}</span>
              <template v-if="ch.uplink || ch.downlink"> · MQTT {{ [ch.uplink && 'up', ch.downlink && 'down'].filter(Boolean).join('/') }}</template>
            </div>
          </div>
          <span v-if="ch.index === 0" class="max-w-44 text-right text-2xs leading-tight text-ink-3 max-sm:hidden">
            {{ multi ? 'The default radio\'s primary · set in the identity editor' : 'Shared primary · set in Configuration → Radios' }}
          </span>
          <template v-else>
            <button v-if="ch.role !== 'DISABLED'" class="btn btn-sm btn-ghost" @click="remove(ch)">Remove</button>
            <button class="btn btn-sm" @click="slotDialog = ch.index">{{ ch.role === 'DISABLED' ? 'Add' : 'Edit' }}</button>
          </template>
        </div>
      </div>

      <div v-else class="space-y-6">
        <section>
          <h3 class="eyebrow mb-2">Export</h3>
          <p class="mb-3 text-[13px] text-ink-3">Scan with the Meshtastic app to add this identity's enabled channels to a phone or another node.</p>
          <div class="flex flex-col items-center gap-4 sm:flex-row sm:items-start">
            <div class="rounded-2xl border border-line-soft bg-white p-2">
              <QrCode v-if="shareUrl" :value="shareUrl" :size="200" />
              <div v-else class="flex size-[200px] items-center justify-center text-ink-3"><QrIcon class="size-8" /></div>
            </div>
            <div class="w-full min-w-0 flex-1">
              <label class="label">Channel URL</label>
              <div class="flex items-center gap-1 rounded-xl border border-line-soft bg-raised px-3 py-2">
                <span class="mono min-w-0 flex-1 break-all text-xs">{{ shareUrl || '…' }}</span>
                <CopyButton v-if="shareUrl" :text="shareUrl" label="Channel URL" />
              </div>
              <ul class="mt-3 space-y-1 text-xs text-ink-2">
                <li v-for="c in identity.channels.filter((c) => c.role !== 'DISABLED')" :key="c.index" class="flex items-center gap-2">
                  <span :class="['chip', roleCls(c.role)]">{{ c.index }}</span>{{ c.display_name }}
                </li>
              </ul>
            </div>
          </div>
        </section>
        <section>
          <h3 class="eyebrow mb-2">Import</h3>
          <p class="mb-2 text-[13px] text-ink-3">Paste a <span class="mono">meshtastic.org/e/#…</span> link. Secondary channels are added to free slots; the primary stays locked.</p>
          <div class="flex gap-2">
            <input v-model="importUrl" class="input mono" placeholder="https://meshtastic.org/e/#…" spellcheck="false" />
            <button class="btn btn-primary shrink-0" :disabled="!importUrl.trim() || importing" @click="doImport"><Spinner v-if="importing" />Import</button>
          </div>
        </section>
      </div>
    </div>
    <ChannelSlotDialog :open="slotDialog !== null" :identity-id="identity?.node_id" :slot="slotDialog ?? undefined" :channels="siteChannels" @close="slotDialog = null" />
  </Drawer>
</template>
