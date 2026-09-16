<script setup lang="ts">
// Identities × channel slots. Slot 0 is each identity's primary; slots 1–7 are added, edited and
// removed here, through the one slot dialog. This page shows every identity on the site; each
// identity's channels are on its own radio, so multi-radio sites get a Radio column.
import { computed, ref } from 'vue'
import { Lock, Plus, QrCode, X } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Channel, Identity } from '@/api/types'
import { live, upsertIdentity } from '@/store/live'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import ChannelsDrawer from '@/components/identities/ChannelsDrawer.vue'
import ChannelSlotDialog, { type SiteChannel } from '@/components/identities/ChannelSlotDialog.vue'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { channelSlots } from '@/lib/channels'

// Every identity's channels, across every radio on the site; grouped by radio on multi-radio sites
// since each identity's channels live on its own radio.
const severalRadios = computed(() => live.radios.length > 1)
const radioOrder = computed(() => new Map(live.radios.map((r, i) => [r.id, i])))
const list = computed(() =>
  [...live.identities].sort(
    (a, b) =>
      (radioOrder.value.get(a.radio_id ?? '') ?? 0) - (radioOrder.value.get(b.radio_id ?? '') ?? 0) ||
      Number(b.is_relay) - Number(a.is_relay) ||
      (a.api?.port ?? 0) - (b.api?.port ?? 0),
  ),
)

// A channel is its name and key.
const chanKey = (c: { name: string; psk: string }) => `${c.name}|${c.psk}`
const palette = ['var(--s1)', 'var(--s2)', 'var(--s5)', 'var(--s7)', 'var(--s4)', 'var(--s6)']
const siteChannels = computed<SiteChannel[]>(() => {
  const map = new Map<string, SiteChannel>()
  for (const i of list.value)
    for (const c of i.channels)
      if (c.role === 'SECONDARY') {
        const key = chanKey(c)
        if (!map.has(key)) map.set(key, { key, name: c.name, psk: c.psk, holders: [] })
        map.get(key)!.holders.push(i.node_id)
      }
  return [...map.values()].sort((a, b) => a.name.localeCompare(b.name))
})
const colours = computed(() => new Map(siteChannels.value.map((c, n) => [c.key, palette[n % palette.length]!])))
/** Describes a channel's key, from its PSK, for the "· default key" style hints. */
function keyKind(psk: string) {
  if (!psk) return 'no encryption'
  if (psk === 'AQ==') return 'default key'
  if (atob(psk).length === 1) return 'default key variant'
  return 'private key'
}

const open = ref<string | null>(null)
const dialog = ref<{ identityId?: string; slot?: number; preset?: SiteChannel | null } | null>(null)

async function disableSlot(i: Identity, c: Channel) {
  upsertIdentity(await api.put<Identity>(`/identities/${enc(i.node_id)}/channels/${c.index}`, { name: '', psk: '', role: 'DISABLED', uplink: false, downlink: false }))
}

async function removeSlot(i: Identity, c: Channel) {
  const ok = await confirmDialog({
    title: `Remove ${c.display_name} from ${i.long_name}?`,
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
    title: `Remove ${ch.name} from every identity?`,
    body: `${holders.map((i) => i.long_name).join(', ')} stop hearing and sending on it. To use it again you'll need its key (${keyKind(ch.psk)}).`,
    confirm: `Remove from ${holders.length}`,
    danger: true,
  })
  if (!ok) return
  try {
    for (const i of holders) for (const c of i.channels) if (c.role === 'SECONDARY' && chanKey(c) === ch.key) await disableSlot(i, c)
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
        </p>
      </div>
      <button type="button" class="btn btn-primary" @click="dialog = {}"><Plus class="size-4" />Add channel to identities</button>
    </div>

    <section class="card overflow-hidden">
      <div class="scroll-thin overflow-x-auto">
        <table class="tbl">
          <thead>
            <tr>
              <th>Identity</th>
              <th v-if="severalRadios">Radio</th>
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
                  </div>
                </div>
              </td>
              <td v-if="severalRadios" class="whitespace-nowrap text-[13px]">
                {{ i.radio_name }}
                <div class="text-2xs text-ink-3">{{ live.radios.find((r) => r.id === i.radio_id)?.phy.preset_name }}</div>
              </td>
              <td v-for="c in channelSlots(i)" :key="c.index" class="!px-1 text-center">
                <div
                  v-if="c.index === 0 && c.role !== 'DISABLED'"
                  class="mx-auto flex h-10 min-w-24 flex-col items-center justify-center rounded-lg border border-brand/25 bg-brand/8 px-2 text-xs font-medium text-brand"
                  :title="`Primary · hash 0x${c.hash.toString(16).padStart(2, '0')}`"
                >
                  <span class="flex items-center gap-1"><Lock class="size-3" />{{ c.display_name }}</span>
                </div>
                <div v-else-if="c.role !== 'DISABLED'" class="mx-auto flex h-10 min-w-24 items-stretch rounded-lg border border-line bg-surface-solid text-xs font-medium">
                  <button type="button" class="flex min-w-0 flex-1 items-center gap-1.5 rounded-l-lg px-2 text-left transition-colors hover:bg-raised" :title="`Edit · hash 0x${c.hash.toString(16).padStart(2, '0')}`" @click="dialog = { identityId: i.node_id, slot: c.index }">
                    <span class="size-2 shrink-0 rounded-full" :style="{ background: colours.get(chanKey(c)) }" />
                    <span class="min-w-0 leading-tight">
                      <span class="block truncate">{{ c.display_name }}</span>
                    </span>
                  </button>
                  <button type="button" class="flex w-6 shrink-0 items-center justify-center rounded-r-lg border-l border-line-soft text-ink-3 hover:bg-bad/10 hover:text-bad" :aria-label="`Remove ${c.display_name} from ${i.long_name}`" title="Remove from this identity" @click="removeSlot(i, c)">
                    <X class="size-3" />
                  </button>
                </div>
                <button
                  v-else-if="c.index > 0"
                  type="button"
                  class="mx-auto flex h-10 w-full min-w-12 items-center justify-center rounded-lg border border-dashed border-line-soft text-ink-3 opacity-60 transition hover:border-line hover:opacity-100"
                  :title="`Add a channel in slot ${c.index}`"
                  :aria-label="`Add a channel to ${i.long_name} in slot ${c.index}`"
                  @click="dialog = { identityId: i.node_id, slot: c.index }"
                >
                  <Plus class="size-3.5" />
                </button>
              </td>
              <td>
                <button type="button" class="icon-btn" title="Share or import channels" @click="open = i.node_id"><QrCode class="size-4" /></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="flex flex-wrap gap-x-5 gap-y-1 border-t border-line-soft px-4 py-2.5 text-xs text-ink-3 sm:px-5">
        <span>Matching dots = the same channel, so those identities hear each other on it.</span>
      </div>
    </section>

    <section v-if="siteChannels.length" class="card mt-4 overflow-hidden">
      <div class="px-4 pb-2 pt-3 sm:px-5"><h3 class="card-title">Channels</h3></div>
      <ul class="divide-y divide-line-soft">
        <li v-for="ch in siteChannels" :key="ch.key" class="flex flex-wrap items-center gap-3 px-4 py-2.5 sm:px-5">
          <span class="size-2.5 shrink-0 rounded-full" :style="{ background: colours.get(ch.key) }" />
          <div class="min-w-0 flex-1">
            <div class="text-[13px] font-medium">{{ ch.name }} <span class="font-normal text-ink-3">· {{ keyKind(ch.psk) }}</span></div>
            <div class="truncate text-xs text-ink-3">{{ list.filter((i) => ch.holders.includes(i.node_id)).map((i) => i.long_name).join(', ') }}</div>
          </div>
          <button type="button" class="btn btn-sm" @click="dialog = { preset: ch }"><Plus class="size-3.5" />Add to…</button>
          <button type="button" class="btn btn-sm btn-ghost hover:!text-bad" @click="removeEverywhere(ch)">Remove from all</button>
        </li>
      </ul>
    </section>

    <ChannelsDrawer :identity-id="open" @close="open = null" />
    <ChannelSlotDialog :open="dialog !== null" :identity-id="dialog?.identityId" :slot="dialog?.slot" :preset="dialog?.preset" :channels="siteChannels" @close="dialog = null" />
  </div>
</template>
