<script setup lang="ts">
// Identities × channel slots. Slot 0 is each identity's primary; slots 1–7 are added, edited and
// removed here, through the one slot dialog. With identities on several radios, every slot is on
// one radio and this page shows the radio you're viewing.
import { computed, ref, watch } from 'vue'
import { Lock, Plus, QrCode, TriangleAlert, X } from '@lucide/vue'
import { api, enc, radio as currentRadio } from '@/api/client'
import type { Channel, Identity } from '@/api/types'
import { live, radioName, refreshAllIdentities, upsertIdentity } from '@/store/live'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import ChannelsDrawer from '@/components/identities/ChannelsDrawer.vue'
import ChannelSlotDialog, { type SiteChannel } from '@/components/identities/ChannelSlotDialog.vue'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { channelSlots } from '@/lib/channels'

const multi = computed(() => live.multiRadioIdentities && live.radios.length > 1)
watch(multi, (m) => m && refreshAllIdentities(), { immediate: true })

const here = computed(() => currentRadio.value || 'main')
const slotRadio = (i: Identity, c: Channel) => c.radio ?? i.radio_id ?? here.value
const isGuest = (i: Identity) => !!i.radio_id && i.radio_id !== here.value

// Identities on this radio: those that live here, plus (with multi-radio on) any whose default
// radio or a slot is here.
const list = computed(() => {
  const local = multi.value ? live.identities.map((i) => live.allIdentities.find((x) => x.node_id === i.node_id) ?? i) : live.identities
  const guests = multi.value ? live.allIdentities.filter((i) => isGuest(i) && (i.radios ?? []).includes(here.value)) : []
  return [...local, ...guests].sort(
    (a, b) => Number(b.is_relay) - Number(a.is_relay) || Number(isGuest(a)) - Number(isGuest(b)) || (a.api?.port ?? 0) - (b.api?.port ?? 0),
  )
})

// A channel is its name, key and (with multi-radio) radio.
const chanKey = (i: Identity, c: Channel) => `${c.name}|${c.psk}${multi.value ? `|${slotRadio(i, c)}` : ''}`
const palette = ['var(--s1)', 'var(--s2)', 'var(--s5)', 'var(--s7)', 'var(--s4)', 'var(--s6)']
const siteChannels = computed<SiteChannel[]>(() => {
  const map = new Map<string, SiteChannel>()
  for (const i of list.value)
    for (const c of i.channels)
      if (c.role === 'SECONDARY') {
        const key = chanKey(i, c)
        if (!map.has(key)) map.set(key, { key, name: c.name, psk: c.psk, radio: multi.value ? slotRadio(i, c) : undefined, holders: [] })
        map.get(key)!.holders.push(i.node_id)
      }
  return [...map.values()].sort((a, b) => a.name.localeCompare(b.name) || (a.radio ?? '').localeCompare(b.radio ?? ''))
})
const colours = computed(() => new Map(siteChannels.value.map((c, n) => [c.key, palette[n % palette.length]!])))
const keyKind = (psk: string) => (!psk ? 'no encryption' : psk === 'AQ==' ? 'default key' : atob(psk).length === 1 ? 'default key variant' : 'private key')

const open = ref<string | null>(null)
const dialog = ref<{ identityId?: string; slot?: number; preset?: SiteChannel | null } | null>(null)

async function disableSlot(i: Identity, c: Channel) {
  upsertIdentity(await api.put<Identity>(`/identities/${enc(i.node_id)}/channels/${c.index}`, { name: '', psk: '', role: 'DISABLED', uplink: false, downlink: false }))
}

async function removeSlot(i: Identity, c: Channel) {
  const where = multi.value ? ` on ${radioName(slotRadio(i, c))}` : ''
  const ok = await confirmDialog({
    title: `Remove ${c.display_name}${where} from ${i.long_name}?`,
    body: `Slot ${c.index} becomes free. ${i.long_name} stops hearing and sending on it, and its app sees the change when it next syncs.`,
    confirm: 'Remove channel',
    danger: true,
  })
  if (!ok) return
  try {
    await disableSlot(i, c)
    toast(`${c.display_name} removed from ${i.long_name}`)
  } catch (e) {
    toastError(e)
  }
}

async function removeEverywhere(ch: SiteChannel) {
  const holders = list.value.filter((i) => ch.holders.includes(i.node_id))
  const ok = await confirmDialog({
    title: `Remove ${ch.name}${ch.radio ? ` on ${radioName(ch.radio)}` : ''} from every identity?`,
    body: `${holders.map((i) => i.long_name).join(', ')} stop hearing and sending on it. To use it again you'll need its key (${keyKind(ch.psk)}).`,
    confirm: `Remove from ${holders.length}`,
    danger: true,
  })
  if (!ok) return
  try {
    for (const i of holders) for (const c of i.channels) if (c.role === 'SECONDARY' && chanKey(i, c) === ch.key) await disableSlot(i, c)
    toast(`${ch.name} removed from ${holders.length} identit${holders.length === 1 ? 'y' : 'ies'}`)
  } catch (e) {
    toastError(e)
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Channels</h2>
        <p class="page-sub">
          Slot 0 is each identity's primary channel and is locked. Slots 1–7: press + to add, click to edit, × to remove.
          <template v-if="multi"> Each slot is on one radio; slots on other radios are shown faded.</template>
        </p>
      </div>
      <button class="btn btn-primary" @click="dialog = {}"><Plus class="size-4" />Add channel to identities</button>
    </div>

    <section class="card overflow-hidden">
      <div class="scroll-thin overflow-x-auto">
        <table class="tbl">
          <thead>
            <tr>
              <th>Identity</th>
              <th v-for="s in 8" :key="s" class="w-[9%] text-center">{{ s - 1 }}</th>
              <th class="w-0"><span class="sr-only">Share</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="i in list" :key="i.node_id">
              <td class="min-w-48">
                <div class="flex items-center gap-2.5">
                  <NodeAvatar :id="i.node_id" :short="i.short_name" size="sm" />
                  <div class="min-w-0 leading-tight">
                    <div class="truncate text-[13px] font-medium">{{ i.long_name }}</div>
                    <div class="mono text-2xs text-ink-3">{{ i.node_id }}</div>
                    <div v-if="multi && isGuest(i)" class="mt-0.5"><span class="chip bg-info/12 text-info">from {{ i.radio_name }}</span></div>
                    <div v-else-if="multi && (i.radios?.length ?? 1) > 1" class="mt-0.5 text-2xs text-ink-3">also on {{ i.radios!.filter((r) => r !== here).map(radioName).join(', ') }}</div>
                  </div>
                </div>
              </td>
              <td v-for="c in channelSlots(i)" :key="c.index" class="!px-1 text-center">
                <!-- slot 0: the primary of the identity's default radio -->
                <div
                  v-if="c.index === 0 && c.role !== 'DISABLED'"
                  :class="['mx-auto flex h-10 min-w-24 flex-col items-center justify-center rounded-lg border border-brand/25 bg-brand/8 px-2 text-xs font-medium text-brand', multi && slotRadio(i, c) !== here && 'opacity-45']"
                  :title="`Primary${multi ? ` on ${radioName(slotRadio(i, c))}` : ''} · hash 0x${c.hash.toString(16).padStart(2, '0')}`"
                >
                  <span class="flex items-center gap-1"><Lock class="size-3" />{{ c.display_name }}</span>
                  <span v-if="multi" class="text-[10px] font-normal opacity-80">{{ slotRadio(i, c) === here ? 'default radio' : `on ${radioName(slotRadio(i, c))}` }}</span>
                </div>
                <div
                  v-else-if="c.role !== 'DISABLED'"
                  :class="[
                    'mx-auto flex h-10 min-w-24 items-stretch rounded-lg border bg-surface-solid text-xs font-medium',
                    c.radio_removed ? 'border-bad' : 'border-line',
                    multi && slotRadio(i, c) !== here && !c.radio_removed && 'border-dashed opacity-45',
                  ]"
                >
                  <button class="flex min-w-0 flex-1 items-center gap-1.5 rounded-l-lg px-2 text-left transition-colors hover:bg-raised" :title="`Edit · hash 0x${c.hash.toString(16).padStart(2, '0')}`" @click="dialog = { identityId: i.node_id, slot: c.index }">
                    <span class="size-2 shrink-0 rounded-full" :style="{ background: colours.get(chanKey(i, c)) }" />
                    <span class="min-w-0 leading-tight">
                      <span class="block truncate">{{ c.display_name }}</span>
                      <span v-if="c.radio_removed" class="block truncate text-[10px] font-normal text-bad">radio removed</span>
                      <span v-else-if="multi" class="block truncate text-[10px] font-normal text-ink-3">{{ slotRadio(i, c) === here ? radioName(here) : `on ${radioName(slotRadio(i, c))}` }}</span>
                    </span>
                  </button>
                  <button class="flex w-6 shrink-0 items-center justify-center rounded-r-lg border-l border-line-soft text-ink-3 hover:bg-bad/10 hover:text-bad" :aria-label="`Remove ${c.display_name} from ${i.long_name}`" title="Remove from this identity" @click="removeSlot(i, c)">
                    <X class="size-3" />
                  </button>
                </div>
                <button
                  v-else-if="c.index > 0"
                  class="mx-auto flex h-10 w-full min-w-12 items-center justify-center rounded-lg border border-dashed border-line-soft text-ink-3 opacity-60 transition hover:border-line hover:opacity-100"
                  :title="`Add a channel in slot ${c.index}`"
                  :aria-label="`Add a channel to ${i.long_name} in slot ${c.index}`"
                  @click="dialog = { identityId: i.node_id, slot: c.index }"
                >
                  <Plus class="size-3.5" />
                </button>
              </td>
              <td>
                <button class="icon-btn" title="Share or import channels" @click="open = i.node_id"><QrCode class="size-4" /></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="flex flex-wrap gap-x-5 gap-y-1 border-t border-line-soft px-4 py-2.5 text-xs text-ink-3 sm:px-5">
        <span>Matching dots = the same channel, so those identities hear each other on it.</span>
        <template v-if="multi">
          <span>Faded, dashed = on another radio.</span>
          <span class="inline-flex items-center gap-1"><TriangleAlert class="size-3 text-bad" />Red edge = its radio was removed; it runs on the default radio until edited.</span>
        </template>
      </div>
    </section>

    <section v-if="siteChannels.length" class="card mt-4 overflow-hidden">
      <div class="px-4 pb-2 pt-3 sm:px-5"><h3 class="card-title">Channels</h3></div>
      <ul class="divide-y divide-line-soft">
        <li v-for="ch in siteChannels" :key="ch.key" class="flex flex-wrap items-center gap-3 px-4 py-2.5 sm:px-5">
          <span class="size-2.5 shrink-0 rounded-full" :style="{ background: colours.get(ch.key) }" />
          <div class="min-w-0 flex-1">
            <div class="text-[13px] font-medium">{{ ch.name }}<template v-if="ch.radio"> · {{ radioName(ch.radio) }}</template> <span class="font-normal text-ink-3">· {{ keyKind(ch.psk) }}</span></div>
            <div class="truncate text-xs text-ink-3">{{ list.filter((i) => ch.holders.includes(i.node_id)).map((i) => i.long_name).join(', ') }}</div>
          </div>
          <button class="btn btn-sm" @click="dialog = { preset: ch }"><Plus class="size-3.5" />Add to…</button>
          <button class="btn btn-sm btn-ghost hover:!text-bad" @click="removeEverywhere(ch)">Remove from all</button>
        </li>
      </ul>
    </section>

    <ChannelsDrawer :identity-id="open" @close="open = null" />
    <ChannelSlotDialog :open="dialog !== null" :identity-id="dialog?.identityId" :slot="dialog?.slot" :preset="dialog?.preset" :channels="siteChannels" @close="dialog = null" />
  </div>
</template>
