<script setup lang="ts">
// Identities × channel slots grid; the shared primary is locked for everyone.
import { computed, ref } from 'vue'
import { Lock, Plus, QrCode } from '@lucide/vue'
import { live } from '@/store/live'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import ChannelsDrawer from '@/components/identities/ChannelsDrawer.vue'

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
const primary = computed(() => live.identities[0]?.channels[0])
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Channels</h2>
        <p class="page-sub">
          Slot 0 is the radio's primary channel <b class="font-medium text-ink-2">{{ primary?.display_name }}</b>, shared and locked for every identity. Slots 1–7 are per identity.
        </p>
      </div>
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
              <td v-for="c in i.channels" :key="c.index" class="!px-1 text-center">
                <div
                  v-if="c.locked"
                  class="mx-auto flex h-9 min-w-24 items-center justify-center gap-1 rounded-lg border border-brand/25 bg-brand/8 px-2 text-xs font-medium text-brand"
                  :title="`Primary · hash 0x${c.hash.toString(16).padStart(2, '0')}`"
                >
                  <Lock class="size-3" />{{ c.display_name }}
                </div>
                <button
                  v-else-if="c.role !== 'DISABLED'"
                  class="mx-auto flex h-9 min-w-24 items-center justify-center gap-1.5 rounded-lg border border-line bg-surface-solid px-2 text-xs font-medium transition-colors hover:bg-raised"
                  :title="`hash 0x${c.hash.toString(16).padStart(2, '0')} · click to edit`"
                  @click="open = { id: i.node_id, focus: c.index }"
                >
                  <span class="size-2 rounded-full" :style="{ background: groups.get(`${c.name}|${c.psk}`) }" />{{ c.display_name }}
                </button>
                <button
                  v-else
                  class="mx-auto flex h-9 w-full min-w-12 items-center justify-center rounded-lg border border-dashed border-line-soft text-ink-3 opacity-60 transition hover:border-line hover:opacity-100"
                  :title="`Add a channel in slot ${c.index}`"
                  @click="open = { id: i.node_id, focus: c.index }"
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

    <ChannelsDrawer :identity-id="open?.id ?? null" :focus="open?.focus" @close="open = null" />
  </div>
</template>
