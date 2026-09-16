<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from 'vue'
import { ArrowDown, ArrowUp, Lock, Search } from '@lucide/vue'
import type { MeshNode } from '@/api/types'
import { live } from '@/store/live'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import NodeDrawer from '@/components/nodes/NodeDrawer.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { now } from '@/composables/now'
import { hwLabel, relTime, roleLabel, snrClass } from '@/lib/format'

const NodeMap = defineAsyncComponent({
  loader: () => import('@/components/nodes/NodeMap.vue'),
  loadingComponent: Spinner,
  delay: 150,
})

type View = 'split' | 'list' | 'map'
function storedView(): View {
  try {
    const v = localStorage.getItem('rt-nodes-view')
    return v === 'list' || v === 'map' ? v : 'split'
  } catch {
    return 'split'
  }
}
const view = ref<View>(storedView())
function setView(v: View) {
  view.value = v
  try {
    localStorage.setItem('rt-nodes-view', v)
  } catch {
    /* ignore */
  }
}

const q = ref('')
const scope = ref<'all' | 'active' | 'local' | 'key'>('all')
type SortKey = 'name' | 'last_heard' | 'snr' | 'hops' | 'role' | 'hw'
const sortKey = ref<SortKey>('last_heard')
const sortDir = ref<1 | -1>(-1)
const selected = ref<string | null>(null)

function sortBy(k: SortKey) {
  if (sortKey.value === k) sortDir.value = sortDir.value === 1 ? -1 : 1
  else {
    sortKey.value = k
    sortDir.value = k === 'name' || k === 'hops' || k === 'role' || k === 'hw' ? 1 : -1
  }
}

const all = computed(() => Object.values(live.nodes))
const filtered = computed(() => {
  const term = q.value.trim().toLowerCase()
  const list = all.value.filter((n) => {
    if (scope.value === 'active' && (n.local || now.value - n.last_heard > 2 * 3600_000)) return false
    if (scope.value === 'local' && !n.local) return false
    if (scope.value === 'key' && !n.has_public_key) return false
    if (!term) return true
    return n.long_name.toLowerCase().includes(term) || n.short_name.toLowerCase().includes(term) || n.node_id.includes(term) || n.hw_model.toLowerCase().includes(term)
  })
  const val = (n: MeshNode): string | number => {
    switch (sortKey.value) {
      case 'name': return n.long_name.toLowerCase()
      case 'last_heard': return n.local ? Infinity : n.last_heard
      case 'snr': return n.snr ?? -999
      case 'hops': return n.hops_away ?? 99
      case 'role': return n.role
      case 'hw': return n.hw_model
    }
  }
  return list.sort((a, b) => {
    const x = val(a), y = val(b)
    return (x < y ? -1 : x > y ? 1 : 0) * sortDir.value
  })
})
const counts = computed(() => ({
  all: all.value.length,
  active: all.value.filter((n) => !n.local && now.value - n.last_heard < 2 * 3600_000).length,
  local: all.value.filter((n) => n.local).length,
  key: all.value.filter((n) => n.has_public_key).length,
  positioned: filtered.value.filter((n) => n.position).length,
}))

const hopCls = (h: number | null) => (h === null ? 'text-ink-3' : h === 0 ? 'text-ink' : 'text-ink-2')
const roleCls = computed(() => (view.value === 'split' ? 'max-md:hidden lg:max-2xl:hidden' : 'max-md:hidden'))
const hwCls = computed(() => (view.value === 'split' ? 'hidden' : 'max-xl:hidden'))
const cols = computed<{ key: SortKey; label: string; cls?: string }[]>(() => [
  { key: 'name', label: 'Node' },
  { key: 'role', label: 'Role', cls: roleCls.value },
  { key: 'hw', label: 'Hardware', cls: hwCls.value },
  { key: 'last_heard', label: 'Last heard' },
  { key: 'snr', label: 'SNR', cls: 'num' },
  { key: 'hops', label: 'Hops', cls: 'num' },
])
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Nodes &amp; map</h2>
        <p class="page-sub">The shared node DB every identity sees · {{ counts.active }} heard in the last 2 h</p>
      </div>
      <div class="seg" role="group" aria-label="Layout">
        <button type="button" :aria-pressed="view === 'split'" class="max-lg:hidden" @click="setView('split')">Split</button>
        <button type="button" :aria-pressed="view === 'list'" @click="setView('list')">List</button>
        <button type="button" :aria-pressed="view === 'map'" @click="setView('map')">Map</button>
      </div>
    </div>

    <div class="mb-3 flex flex-wrap items-center gap-2">
      <div class="relative w-full sm:w-72">
        <Search class="pointer-events-none absolute left-3 top-2.5 size-4 text-ink-3" />
        <input id="nodes-search" v-model="q" aria-label="Search nodes" class="input pl-9" placeholder="Search name, id or hardware" />
      </div>
      <div class="seg">
        <button type="button" :aria-pressed="scope === 'all'" @click="scope = 'all'">All {{ counts.all }}</button>
        <button type="button" :aria-pressed="scope === 'active'" @click="scope = 'active'">Active {{ counts.active }}</button>
        <button type="button" :aria-pressed="scope === 'local'" @click="scope = 'local'">Local {{ counts.local }}</button>
        <button type="button" :aria-pressed="scope === 'key'" @click="scope = 'key'">With key {{ counts.key }}</button>
      </div>
    </div>

    <div :class="['grid gap-4', view === 'split' ? 'lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)]' : '']">
      <section v-if="view !== 'map'" :class="['card overflow-hidden', view === 'split' ? 'lg:h-[calc(100dvh-14rem)] lg:min-h-[520px]' : '']">
        <div :class="['scroll-thin overflow-auto', view === 'split' ? 'max-h-[70dvh] lg:h-full lg:max-h-none' : '']">
          <table class="tbl">
            <thead>
              <tr>
                <th v-for="c in cols" :key="c.key" :class="c.cls">
                  <button type="button" class="inline-flex items-center gap-1 uppercase hover:text-ink" @click="sortBy(c.key)">
                    {{ c.label }}
                    <template v-if="sortKey === c.key"><ArrowUp v-if="sortDir === 1" class="size-3" /><ArrowDown v-else class="size-3" /></template>
                  </button>
                </th>
                <th class="text-center">Key</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="n in filtered"
                :key="n.node_id"
                :class="['row-link', selected === n.node_id ? '!bg-brand/8' : '']"
                @click="selected = n.node_id"
              >
                <td class="min-w-52">
                  <div class="flex items-center gap-2.5">
                    <NodeAvatar :id="n.node_id" :short="n.short_name" size="sm" :local="n.local" />
                    <div class="min-w-0 leading-tight">
                      <div class="truncate font-medium">{{ n.long_name }}</div>
                      <div class="mono text-2xs text-ink-3">{{ n.node_id }}<span v-if="n.via_mqtt" class="font-sans"> · MQTT</span></div>
                    </div>
                  </div>
                </td>
                <td :class="['text-ink-2', roleCls]">{{ roleLabel(n.role) }}</td>
                <td :class="['text-ink-2', hwCls]">{{ hwLabel(n.hw_model) }}</td>
                <td class="whitespace-nowrap tabular-nums text-ink-2">
                  <span v-if="n.local" class="chip bg-brand/14 text-brand">local</span>
                  <span v-else :class="now - n.last_heard > 2 * 3600_000 ? 'text-ink-3' : ''">{{ relTime(n.last_heard, now) }}</span>
                </td>
                <td :class="['num', snrClass(n.snr)]">{{ n.snr?.toFixed(1) ?? '—' }}</td>
                <td :class="['num', hopCls(n.hops_away)]">{{ n.hops_away ?? '—' }}</td>
                <td class="text-center">
                  <Lock v-if="n.has_public_key" class="inline size-3.5 text-ok" aria-label="public key known" />
                  <span v-else class="text-ink-3">—</span>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!filtered.length" class="empty">No nodes match.</div>
        </div>
      </section>

      <section v-if="view !== 'list'" :class="['card relative overflow-hidden', view === 'split' ? 'h-[420px] lg:h-[calc(100dvh-14rem)] lg:min-h-[520px]' : 'h-[calc(100dvh-14rem)] min-h-[420px]']">
        <NodeMap :nodes="filtered" :selected="selected" @select="selected = $event" />
        <div class="pointer-events-none absolute bottom-2 left-2 z-[500] rounded-lg border border-line bg-surface-solid/90 px-2.5 py-1.5 text-2xs text-ink-2 backdrop-blur">
          <div class="mb-1 font-medium text-ink">{{ counts.positioned }} with position</div>
          <div class="flex flex-wrap gap-x-3 gap-y-0.5">
            <span class="inline-flex items-center gap-1"><span class="dot bg-brand" />local</span>
            <span class="inline-flex items-center gap-1"><span class="dot bg-s1" />direct</span>
            <span class="inline-flex items-center gap-1"><span class="dot bg-s3" />1 hop</span>
            <span class="inline-flex items-center gap-1"><span class="dot bg-s4" />2 hops</span>
            <span class="inline-flex items-center gap-1"><span class="dot bg-s5" />3+</span>
          </div>
        </div>
      </section>
    </div>

    <NodeDrawer :node-id="selected" @close="selected = null" />
  </div>
</template>
