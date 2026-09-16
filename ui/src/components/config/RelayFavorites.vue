<script setup lang="ts">
// The favourites of a client_base relay: packets from or to them are relayed like router_late.
// This host's identities always count; other nodes are picked here.
import { computed, onMounted, ref } from 'vue'
import { api } from '@/api/client'
import type { MeshNode } from '@/api/types'
import { relTime } from '@/lib/format'
import { now } from '@/composables/now'

const favorites = defineModel<string[]>({ required: true })
const nodes = ref<MeshNode[]>([])
const filter = ref('')
const loaded = ref(false)

onMounted(async () => {
  try {
    const list = await api.get<MeshNode[] | { nodes: MeshNode[] }>('/nodes')
    nodes.value = Array.isArray(list) ? list : list.nodes
  } catch {
    nodes.value = []
  } finally {
    loaded.value = true
  }
})

const own = computed(() => nodes.value.filter((n) => n.local))
const others = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const picked = new Set(favorites.value)
  return nodes.value
    .filter((n) => !n.local)
    .filter((n) => !q || n.long_name.toLowerCase().includes(q) || n.short_name.toLowerCase().includes(q) || n.node_id.includes(q))
    .sort((a, b) => Number(picked.has(b.node_id)) - Number(picked.has(a.node_id)) || b.last_heard - a.last_heard)
    .slice(0, 60)
})
// Favourites not heard lately still count: keep them visible so they can be removed.
const unknown = computed(() => favorites.value.filter((id) => !nodes.value.some((n) => n.node_id === id)))

function toggle(id: string, on: boolean) {
  const set = new Set(favorites.value)
  if (on) set.add(id)
  else set.delete(id)
  favorites.value = [...set]
}
</script>

<template>
  <div class="rounded-xl border border-line-soft px-3.5 py-3">
    <div class="flex flex-wrap items-baseline justify-between gap-2">
      <span class="label !mb-0">Favourite nodes</span>
      <span class="text-2xs text-ink-3">{{ favorites.length }} picked</span>
    </div>
    <p class="hint !mt-1">
      As client base, the relay repeats packets from or to its favourites like a late router, and everything else like a client. Pick your own
      nodes that rely on this site, such as a handheld or an indoor node.
    </p>
    <div v-if="own.length" class="mt-2 flex flex-wrap items-center gap-1.5 text-2xs">
      <span class="text-ink-3">Always included:</span>
      <span v-for="n in own" :key="n.node_id" class="chip bg-brand/12 text-brand">{{ n.long_name || n.node_id }}</span>
    </div>
    <input id="c-fav-filter" v-model="filter" class="input mt-2 h-8 text-xs" placeholder="Find a node by name or ID" aria-label="Find a node" />
    <ul class="scroll-thin mt-2 max-h-56 space-y-0.5 overflow-y-auto">
      <li v-for="id in unknown" :key="id">
        <label class="flex cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 text-xs hover:bg-raised">
          <input type="checkbox" checked class="accent-[var(--brand)]" @change="toggle(id, ($event.target as HTMLInputElement).checked)" />
          <span class="mono">{{ id }}</span><span class="text-ink-3">not heard lately</span>
        </label>
      </li>
      <li v-for="n in others" :key="n.node_id">
        <label class="flex cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 text-xs hover:bg-raised">
          <input type="checkbox" :checked="favorites.includes(n.node_id)" class="accent-[var(--brand)]" @change="toggle(n.node_id, ($event.target as HTMLInputElement).checked)" />
          <span class="min-w-0 truncate font-medium">{{ n.long_name || n.node_id }}</span>
          <span class="mono shrink-0 text-ink-3">{{ n.node_id }}</span>
          <span class="ml-auto shrink-0 text-ink-3">{{ n.hops_away === 0 ? 'direct' : n.hops_away != null ? `${n.hops_away} hops` : '' }} · {{ relTime(n.last_heard, now) }}</span>
        </label>
      </li>
      <li v-if="loaded && !others.length && !unknown.length" class="px-1.5 py-1 text-xs text-ink-3">No other nodes heard yet.</li>
    </ul>
  </div>
</template>
