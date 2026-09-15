<script setup lang="ts">
// Channels editor for one identity: slot 0 is the shared primary (locked), 1–7 are per-identity secondaries.
import { computed, ref, watch } from 'vue'
import { Dices, Lock, QrCode as QrIcon } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Channel, ChannelRole, Identity } from '@/api/types'
import Drawer from '@/components/ui/Drawer.vue'
import Toggle from '@/components/ui/Toggle.vue'
import QrCode from '@/components/ui/QrCode.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { live, upsertIdentity } from '@/store/live'
import { toast, toastError } from '@/composables/toast'
import { channelSlots } from '@/lib/channels'

const props = defineProps<{ identityId: string | null; focus?: number }>()
const emit = defineEmits<{ close: [] }>()

const identity = computed<Identity | undefined>(() => live.identities.find((i) => i.node_id === props.identityId) ?? live.allIdentities.find((i) => i.node_id === props.identityId))
const tab = ref<'edit' | 'share'>('edit')
const editing = ref<number | null>(null)
const draft = ref<{ name: string; psk: string; role: ChannelRole; uplink: boolean; downlink: boolean }>({ name: '', psk: '', role: 'SECONDARY', uplink: false, downlink: false })
const saving = ref(false)
const shareUrl = ref('')
const importUrl = ref('')
const importing = ref(false)

watch(
  () => props.identityId,
  (id) => {
    tab.value = 'edit'
    editing.value = null
    shareUrl.value = ''
    importUrl.value = ''
    if (id && props.focus !== undefined && props.focus > 0) setTimeout(() => startEdit(props.focus!), 0)
  },
)

function startEdit(index: number) {
  const ch = identity.value ? channelSlots(identity.value)[index] : undefined
  if (!ch || ch.locked) return
  editing.value = index
  draft.value = { name: ch.name, psk: ch.psk, role: ch.role === 'DISABLED' ? 'SECONDARY' : ch.role, uplink: ch.uplink, downlink: ch.downlink }
}

function randomPsk() {
  const b = new Uint8Array(32)
  crypto.getRandomValues(b)
  draft.value.psk = btoa(String.fromCharCode(...b))
}

const pskKind = (psk: string) => {
  if (!psk) return 'no encryption'
  if (psk === 'AQ==') return 'default key'
  const len = atob(psk).length
  return len === 1 ? `default key #${atob(psk).charCodeAt(0)}` : `AES-${len * 8}`
}

const draftValid = computed(() => {
  const d = draft.value
  if (d.role !== 'DISABLED' && !d.name.trim()) return false
  if (new TextEncoder().encode(d.name).length > 11) return false
  if (!d.psk) return true
  try {
    return [0, 1, 16, 32].includes(atob(d.psk).length)
  } catch {
    return false
  }
})

async function saveChannel(index: number, body: Partial<Channel>) {
  if (!identity.value) return
  saving.value = true
  try {
    upsertIdentity(await api.put<Identity>(`/identities/${enc(identity.value.node_id)}/channels/${index}`, body))
    editing.value = null
    toast(`Channel ${index} saved`)
  } catch (e) {
    toastError(e)
  } finally {
    saving.value = false
  }
}

function disable(ch: Channel) {
  saveChannel(ch.index, { name: '', psk: '', role: 'DISABLED', uplink: false, downlink: false })
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
        <div
          v-for="ch in channelSlots(identity)"
          :key="ch.index"
          :class="['rounded-xl border transition-colors', editing === ch.index ? 'border-brand/50 bg-brand/5' : 'border-line-soft bg-raised/60']"
        >
          <div class="flex items-center gap-3 px-3.5 py-2.5">
            <span class="flex size-7 shrink-0 items-center justify-center rounded-lg bg-sunken text-xs font-semibold tabular-nums text-ink-2">{{ ch.index }}</span>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span :class="['truncate text-[13px] font-medium', ch.role === 'DISABLED' && 'text-ink-3']">{{ ch.display_name || (ch.role === 'DISABLED' ? 'Unused' : '(no name)') }}</span>
                <span :class="['chip', roleCls(ch.role)]">{{ ch.role.toLowerCase() }}</span>
                <Lock v-if="ch.locked" class="size-3.5 text-ink-3" />
              </div>
              <div v-if="ch.role !== 'DISABLED'" class="mt-0.5 text-xs text-ink-3">
                {{ pskKind(ch.psk) }} · hash <span class="mono">0x{{ ch.hash.toString(16).padStart(2, '0') }}</span>
                <template v-if="ch.uplink || ch.downlink"> · MQTT {{ [ch.uplink && 'up', ch.downlink && 'down'].filter(Boolean).join('/') }}</template>
              </div>
            </div>
            <template v-if="ch.locked">
              <span class="max-w-40 text-right text-2xs leading-tight text-ink-3 max-sm:hidden">Shared primary · set in <RouterLink to="/config/radio" class="text-brand hover:underline">Radio config</RouterLink></span>
            </template>
            <template v-else-if="editing !== ch.index">
              <button v-if="ch.role !== 'DISABLED'" class="btn btn-sm btn-ghost" @click="disable(ch)">Disable</button>
              <button class="btn btn-sm" @click="startEdit(ch.index)">{{ ch.role === 'DISABLED' ? 'Add' : 'Edit' }}</button>
            </template>
          </div>

          <div v-if="editing === ch.index" class="grid gap-3 border-t border-line-soft px-3.5 pb-3.5 pt-3 sm:grid-cols-2">
            <div>
              <label class="label" :for="`cn${ch.index}`">Name</label>
              <input :id="`cn${ch.index}`" v-model="draft.name" class="input" maxlength="11" placeholder="e.g. LothianOps" />
            </div>
            <div>
              <label class="label" :for="`cr${ch.index}`">Role</label>
              <select :id="`cr${ch.index}`" v-model="draft.role" class="input">
                <option value="SECONDARY">Secondary</option>
                <option value="DISABLED">Disabled</option>
              </select>
            </div>
            <div class="sm:col-span-2">
              <label class="label" :for="`ck${ch.index}`">Pre-shared key (base64) · {{ pskKind(draft.psk) }}</label>
              <div class="flex gap-2">
                <input :id="`ck${ch.index}`" v-model="draft.psk" class="input mono" spellcheck="false" placeholder="empty = unencrypted" />
                <button class="btn shrink-0" title="Random AES-256 key" @click="randomPsk"><Dices class="size-4" /></button>
                <button class="btn shrink-0" title="Meshtastic default key" @click="draft.psk = 'AQ=='">AQ==</button>
              </div>
            </div>
            <div class="flex flex-wrap items-center gap-5 text-[13px] sm:col-span-2">
              <label class="flex items-center gap-2"><Toggle v-model="draft.uplink" label="MQTT uplink" />MQTT uplink</label>
              <label class="flex items-center gap-2"><Toggle v-model="draft.downlink" label="MQTT downlink" />MQTT downlink</label>
              <div class="ml-auto flex gap-2">
                <button class="btn btn-sm" @click="editing = null">Cancel</button>
                <button class="btn btn-sm btn-primary" :disabled="!draftValid || saving" @click="saveChannel(ch.index, { ...draft, name: draft.name.trim() })">
                  <Spinner v-if="saving" />Save
                </button>
              </div>
            </div>
          </div>
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
  </Drawer>
</template>
