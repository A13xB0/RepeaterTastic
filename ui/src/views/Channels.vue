<script setup lang="ts">
// Identities × channel slots grid; the shared primary is locked for everyone.
import { computed, ref } from 'vue'
import { Lock, Plus, QrCode, X } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Channel, Identity } from '@/api/types'
import { live, upsertIdentity } from '@/store/live'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import ChannelsDrawer from '@/components/identities/ChannelsDrawer.vue'
import AddChannelModal, { type SiteChannel } from '@/components/identities/AddChannelModal.vue'
import { confirmDialog } from '@/composables/confirm'
import { channelSlots } from '@/lib/channels'
import { toast, toastError } from '@/composables/toast'

const open = ref<{ id: string; focus?: number } | null>(null)
const slots = [0, 1, 2, 3, 4, 5, 6, 7]
const list = computed(() => [...live.identities].sort((a, b) => Number(b.is_relay) - Number(a.is_relay) || (a.api?.port ?? 0) - (b.api?.port ?? 0)))

// Channels with the same name + key on several identities share a colour.
const palette = ['var(--s1)', 'var(--s2)', 'var(--s5)', 'var(--s7)', 'var(--s4)', 'var(--s6)']
const groups = computed(() => {
  const map = new Map<string, string>()
  for (const i of live.identities)
    for (const c of i.channels)
      if (c.role === 'SECONDARY') {
        const k = `${c.name}|${c.psk}`
        if (!map.has(k)) map.set(k, palette[map.size % palette.length]!)
      }
  return map
})
const primary = computed(() => live.identities[0]?.channels.find((c) => c.index === 0))

// Every secondary channel on this radio (same name and key = one channel), with who holds it.
const siteChannels = computed<SiteChannel[]>(() => {
  const map = new Map<string, SiteChannel>()
  for (const i of list.value)
    for (const c of i.channels)
      if (c.role === 'SECONDARY') {
        const key = `${c.name}|${c.psk}`
        if (!map.has(key)) map.set(key, { key, name: c.name, psk: c.psk, holders: [] })
        map.get(key)!.holders.push(i.node_id)
      }
  return [...map.values()].sort((a, b) => a.name.localeCompare(b.name))
})
const keyKind = (psk: string) => (!psk ? 'no encryption' : psk === 'AQ==' ? 'default key' : atob(psk).length === 1 ? 'default key variant' : 'private key')

const adding = ref<{ identityId?: string; slot?: number; preset?: SiteChannel | null } | null>(null)

async function disableSlot(i: Identity, c: Channel) {
  await api.put<Identity>(`/identities/${enc(i.node_id)}/channels/${c.index}`, { name: '', psk: '', role: 'DISABLED', uplink: false, downlink: false }).then(upsertIdentity)
}

async function removeSlot(i: Identity, c: Channel) {
  const ok = await confirmDialog({
    title: `Remove ${c.display_name} from ${i.long_name}?`,
    body: `Slot ${c.index} becomes free. ${i.long_name} stops hearing and sending on ${c.display_name}, and its app sees the change when it next syncs.`,
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
    body: `${holders.map((i) => i.long_name).join(', ')} stop hearing and sending on ${ch.name}. To use it again you'll need its key (${keyKind(ch.psk)}).`,
    confirm: `Remove from ${holders.length}`,
    danger: true,
  })
  if (!ok) return
  try {
    for (const i of holders) for (const c of i.channels) if (c.role === 'SECONDARY' && c.name === ch.name && c.psk === ch.psk) await disableSlot(i, c)
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
          Slot 0 is the radio's primary channel <b class="font-medium text-ink-2">{{ primary?.display_name }}</b>, shared and locked for every identity. Slots 1–7 are per identity: press + to add, × to remove.
        </p>
      </div>
      <button class="btn btn-primary" @click="adding = {}"><Plus class="size-4" />Add channel to identities</button>
    </div>

    <section class="card overflow-hidden">
      <div class="scroll-thin overflow-x-auto">
        <table class="tbl">
          <thead>
            <tr>
              <th>Identity</th>
              <th v-for="s in slots" :key="s" class="w-[9%] text-center">{{ s }}</th>
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
              <td v-for="c in channelSlots(i)" :key="c.index" class="!px-1 text-center">
                <div
                  v-if="c.locked"
                  class="mx-auto flex h-9 min-w-24 items-center justify-center gap-1 rounded-lg border border-brand/25 bg-brand/8 px-2 text-xs font-medium text-brand"
                  :title="`Primary · hash 0x${c.hash.toString(16).padStart(2, '0')}`"
                >
                  <Lock class="size-3" />{{ c.display_name }}
                </div>
                <div v-else-if="c.role !== 'DISABLED'" class="group relative mx-auto flex h-9 min-w-24 items-stretch rounded-lg border border-line bg-surface-solid text-xs font-medium">
                  <button
                    class="flex min-w-0 flex-1 items-center justify-center gap-1.5 rounded-l-lg px-2 transition-colors hover:bg-raised"
                    :title="`hash 0x${c.hash.toString(16).padStart(2, '0')} · edit`"
                    @click="open = { id: i.node_id, focus: c.index }"
                  >
                    <span class="size-2 shrink-0 rounded-full" :style="{ background: groups.get(`${c.name}|${c.psk}`) }" /><span class="truncate">{{ c.display_name }}</span>
                  </button>
                  <button class="flex w-6 shrink-0 items-center justify-center rounded-r-lg border-l border-line-soft text-ink-3 hover:bg-bad/10 hover:text-bad" :aria-label="`Remove ${c.display_name} from ${i.long_name}`" title="Remove from this identity" @click="removeSlot(i, c)">
                    <X class="size-3" />
                  </button>
                </div>
                <button
                  v-else
                  class="mx-auto flex h-9 w-full min-w-12 items-center justify-center rounded-lg border border-dashed border-line-soft text-ink-3 opacity-60 transition hover:border-line hover:opacity-100"
                  :title="`Add a channel in slot ${c.index}`"
                  :aria-label="`Add a channel to ${i.long_name} in slot ${c.index}`"
                  @click="adding = { identityId: i.node_id, slot: c.index }"
                >
                  <Plus class="size-3.5" />
                </button>
              </td>
              <td>
                <button class="icon-btn" title="Share or import channels" @click="open = { id: i.node_id }"><QrCode class="size-4" /></button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="border-t border-line-soft px-4 py-2.5 text-xs text-ink-3 sm:px-5">
        Matching dots mean the same name and key, so those identities hear each other on that channel.
      </div>
    </section>

    <section v-if="siteChannels.length" class="card mt-4 overflow-hidden">
      <div class="px-4 pb-2 pt-3 sm:px-5"><h3 class="card-title">Channels on this radio</h3></div>
      <ul class="divide-y divide-line-soft">
        <li v-for="ch in siteChannels" :key="ch.key" class="flex flex-wrap items-center gap-3 px-4 py-2.5 sm:px-5">
          <span class="size-2.5 shrink-0 rounded-full" :style="{ background: groups.get(ch.key) }" />
          <div class="min-w-0 flex-1">
            <div class="text-[13px] font-medium">{{ ch.name }} <span class="font-normal text-ink-3">· {{ keyKind(ch.psk) }}</span></div>
            <div class="truncate text-xs text-ink-3">{{ list.filter((i) => ch.holders.includes(i.node_id)).map((i) => i.long_name).join(', ') }}</div>
          </div>
          <button class="btn btn-sm" :disabled="ch.holders.length >= list.length" @click="adding = { preset: ch }"><Plus class="size-3.5" />Add to…</button>
          <button class="btn btn-sm btn-ghost hover:!text-bad" @click="removeEverywhere(ch)">Remove from all</button>
        </li>
      </ul>
    </section>

    <ChannelsDrawer :identity-id="open?.id ?? null" :focus="open?.focus" @close="open = null" />
    <AddChannelModal :open="adding !== null" :identity-id="adding?.identityId" :slot="adding?.slot" :preset="adding?.preset" :channels="siteChannels" @close="adding = null" />
  </div>
</template>
