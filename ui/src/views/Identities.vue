<script setup lang="ts">
// Identities: the Meshtastic counterpart of openHop's Companions view.
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Import, KeyRound, Layers, MessagesSquare, Pencil, Plus, RotateCcw, Trash } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Identity } from '@/api/types'
import { live, refreshAllIdentities, removeIdentity, upsertIdentity } from '@/store/live'
import NodeAvatar from '@/components/ui/NodeAvatar.vue'
import Toggle from '@/components/ui/Toggle.vue'
import CopyButton from '@/components/ui/CopyButton.vue'
import CreateIdentityModal from '@/components/identities/CreateIdentityModal.vue'
import EditIdentityModal from '@/components/identities/EditIdentityModal.vue'
import KeyModal from '@/components/identities/KeyModal.vue'
import ChannelsDrawer from '@/components/identities/ChannelsDrawer.vue'
import { confirmDialog } from '@/composables/confirm'
import { toast, toastError } from '@/composables/toast'
import { roleLabel, seconds } from '@/lib/format'

const createMode = ref<'create' | 'import' | null>(null)
const editing = ref<Identity | null>(null)
const keyFor = ref<Identity | null>(null)
const channelsFor = ref<string | null>(null)

// With several radios the list can show this radio's identities or every radio's.
const multiRadio = computed(() => live.radios.length > 1)
const scope = ref<'radio' | 'all'>('radio')
const source = computed(() => (multiRadio.value && scope.value === 'all' ? live.allIdentities : live.identities))
const radioOrder = computed(() => new Map(live.radios.map((r, i) => [r.id, i])))
const list = computed(() =>
  [...source.value].sort(
    (a, b) =>
      (radioOrder.value.get(a.radio_id ?? '') ?? 0) - (radioOrder.value.get(b.radio_id ?? '') ?? 0) ||
      Number(b.is_relay) - Number(a.is_relay) ||
      (a.api?.port ?? 0) - (b.api?.port ?? 0),
  ),
)
watch(scope, (s) => s === 'all' && refreshAllIdentities())
let allTimer: number | undefined
onMounted(() => {
  allTimer = window.setInterval(() => scope.value === 'all' && refreshAllIdentities(), 10_000)
})
onBeforeUnmount(() => clearInterval(allTimer))
const budgetMs = computed(() => ((live.status?.airtime.duty_limit_pct ?? 10) / 100) * (live.status?.airtime.window_s ?? 3600) * 1000)
const totals = computed(() => ({
  apps: source.value.reduce((s, i) => s + (i.api?.clients ?? 0), 0),
  outbox: source.value.reduce((s, i) => s + i.outbox, 0),
  airtime: source.value.reduce((s, i) => s + i.airtime_ms_1h, 0),
}))

type State = { label: string; cls: string; title: string }
function stateOf(i: Identity): State {
  const budgetPct = (i.airtime_ms_1h / budgetMs.value) * 100
  if (i.is_relay) {
    const role = live.status?.relay.role
    if (role === 'off') return { label: 'Radio off', cls: 'bg-bad/12 text-bad', title: 'The radio is off: nothing is received or sent' }
    if (role === 'monitor') return { label: 'Listening', cls: 'bg-info/12 text-info', title: 'Monitor mode: the radio only listens' }
    return role === 'mute'
      ? { label: 'Muted', cls: 'bg-bad/12 text-bad', title: 'Relay mode is mute: nothing is rebroadcast' }
      : { label: 'Relaying', cls: 'bg-brand/14 text-brand', title: `Relay mode ${role}` }
  }
  if (!i.enabled) return { label: 'Disabled', cls: 'bg-ink-3/14 text-ink-3', title: 'Not transmitting; API port closed' }
  if (i.share_limit_pct && budgetPct > i.share_limit_pct)
    return { label: 'Over share', cls: 'bg-warn/15 text-warn', title: `Using ${budgetPct.toFixed(0)}% of the duty budget (limit ${i.share_limit_pct}%)` }
  if ((i.api?.clients ?? 0) > 0) return { label: 'Online', cls: 'bg-ok/14 text-ok', title: `${i.api?.clients} app(s) connected` }
  return { label: 'No app', cls: 'bg-info/12 text-info', title: 'Running; no client app connected' }
}

function budgetBar(i: Identity) {
  const pct = (i.airtime_ms_1h / budgetMs.value) * 100
  const limit = i.is_relay ? 100 : (i.share_limit_pct ?? 100)
  return { width: Math.min(100, pct), limit: Math.min(100, limit), over: !i.is_relay && pct > limit, pct }
}

async function setEnabled(i: Identity, enabled: boolean) {
  try {
    upsertIdentity(await api.patch<Identity>(`/identities/${enc(i.node_id)}`, { enabled }))
    toast(`${i.long_name} ${enabled ? 'enabled' : 'disabled'}`)
  } catch (e) {
    toastError(e)
  }
}

async function restartApi(i: Identity) {
  try {
    await api.post(`/identities/${enc(i.node_id)}/api/restart`)
    toast(`API :${i.api?.port} restarted`)
  } catch (e) {
    toastError(e)
  }
}

async function remove(i: Identity) {
  const ok = await confirmDialog({
    title: `Delete ${i.long_name}?`,
    body: `${i.node_id} and its private key will be removed from this host, and apps connected to :${i.api?.port} will be dropped. Other nodes keep it in their node list until it ages out. Back up the key first if you may want this node back.`,
    confirm: 'Delete identity',
    danger: true,
  })
  if (!ok) return
  try {
    await api.del(`/identities/${enc(i.node_id)}`)
    removeIdentity(i.node_id)
    toast(`${i.long_name} deleted`)
  } catch (e) {
    toastError(e)
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2 class="page-title">Identities</h2>
        <p class="page-sub">
          {{ source.length }} nodes {{ multiRadio && scope === 'all' ? `on ${live.radios.length} radios` : 'on this modem' }} · {{ totals.apps }} apps connected · {{ seconds(totals.airtime) }} airtime in the last hour
        </p>
      </div>
      <div class="flex flex-wrap gap-2">
        <div v-if="multiRadio" class="tabs-pill flex rounded-lg border border-line-soft p-0.5" role="group" aria-label="Which identities">
          <button v-for="o in [{ v: 'radio', l: 'This radio' }, { v: 'all', l: 'All radios' }] as const" :key="o.v" type="button"
            :class="['rounded-md px-2.5 py-1 text-xs font-medium', scope === o.v ? 'bg-raised text-ink shadow-sm' : 'text-ink-3 hover:text-ink']"
            :aria-pressed="scope === o.v" @click="scope = o.v">{{ o.l }}</button>
        </div>
        <button class="btn" @click="createMode = 'import'"><Import class="size-4" />Import key</button>
        <button class="btn btn-primary" @click="createMode = 'create'"><Plus class="size-4" />New identity</button>
      </div>
    </div>

    <section class="card overflow-hidden">
      <div class="scroll-thin overflow-x-auto">
        <table class="tbl">
          <thead>
            <tr>
              <th>Node</th>
              <th>API</th>
              <th class="num">Apps</th>
              <th class="num max-2xl:hidden">Outbox</th>
              <th class="max-xl:hidden">Airtime 1 h</th>
              <th>Status</th>
              <th class="max-lg:hidden">Channels</th>
              <th class="w-0"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="i in list" :key="i.node_id" :class="!i.enabled && !i.is_relay ? 'opacity-70' : ''">
              <td class="min-w-56">
                <div class="flex items-center gap-3">
                  <NodeAvatar :id="i.node_id" :short="i.short_name" />
                  <div class="min-w-0 leading-tight">
                    <div class="flex items-center gap-1.5 truncate text-[13px] font-semibold">{{ i.long_name }}</div>
                    <div class="flex items-center gap-1 text-xs text-ink-3">
                      <span class="mono">{{ i.node_id }}</span>
                      <CopyButton :text="i.node_id" label="Node id" />
                      <span>· {{ i.is_relay ? 'relay persona' : roleLabel(i.role) }}</span>
                    </div>
                    <div v-if="multiRadio" class="mt-1 flex flex-wrap gap-1">
                      <span class="chip bg-ink-3/12 text-ink-2" :title="`Home radio ${i.radio_name}`">{{ i.radio_name }}</span>
                      <span v-if="(i.radios?.length ?? 1) > 1" class="chip bg-info/12 text-info" :title="`Also on ${i.radios!.slice(1).map((r) => live.radios.find((x) => x.id === r)?.name ?? r).join(', ')} (experimental)`">+{{ i.radios!.length - 1 }} radio{{ i.radios!.length > 2 ? 's' : '' }}</span>
                    </div>
                  </div>
                </div>
              </td>
              <td class="whitespace-nowrap">
                <template v-if="i.api">
                  <span class="mono font-medium">:{{ i.api.port }}</span>
                  <div class="text-2xs text-ink-3">{{ i.api.bind }}</div>
                </template>
                <span v-else class="text-ink-3">—</span>
              </td>
              <td class="num">
                <span v-if="i.api" :class="i.api.clients ? 'font-medium' : 'text-ink-3'">{{ i.api.clients }}</span>
                <span v-else class="text-ink-3">—</span>
              </td>
              <td class="num max-2xl:hidden">
                <span v-if="!i.is_relay" :class="i.outbox > 0 ? 'font-medium text-warn' : 'text-ink-3'">{{ i.outbox }}</span>
                <span v-else class="text-ink-3">—</span>
              </td>
              <td class="min-w-40 max-xl:hidden">
                <div class="flex items-baseline justify-between gap-2 text-xs">
                  <span class="font-medium tabular-nums">{{ seconds(i.airtime_ms_1h) }}</span>
                  <span class="tabular-nums text-ink-3">{{ budgetBar(i).pct.toFixed(1) }}% of budget</span>
                </div>
                <div class="relative mt-1 h-1.5 rounded-full bg-ink-3/15">
                  <div :class="['h-full rounded-full', budgetBar(i).over ? 'bg-warn' : 'bg-brand']" :style="{ width: `${Math.max(budgetBar(i).width, 1)}%` }" />
                  <span v-if="!i.is_relay" class="absolute -top-0.5 h-2.5 w-0.5 rounded bg-ink-3/60" :style="{ left: `${budgetBar(i).limit}%` }" :title="`share limit ${i.share_limit_pct}%`" />
                </div>
              </td>
              <td>
                <span :class="['chip', stateOf(i).cls]" :title="stateOf(i).title">{{ stateOf(i).label }}</span>
              </td>
              <td class="max-lg:hidden">
                <button class="flex flex-wrap gap-1" title="Edit channels" @click="channelsFor = i.node_id">
                  <span
                    v-for="c in i.channels.filter((c) => c.role !== 'DISABLED')"
                    :key="c.index"
                    :class="['chip', c.role === 'PRIMARY' ? 'bg-brand/12 text-brand' : 'bg-info/12 text-info']"
                  >{{ c.display_name }}</span>
                </button>
              </td>
              <td>
                <div class="flex items-center justify-end gap-0.5">
                  <Toggle v-if="!i.is_relay" class="mr-2" :model-value="i.enabled" :label="`${i.enabled ? 'Disable' : 'Enable'} ${i.long_name}`" @update:model-value="setEnabled(i, $event)" />
                  <RouterLink :to="`/chat/${i.node_id}`" class="icon-btn" :title="i.is_relay ? 'Chat as the relay persona' : 'Open chat'"><MessagesSquare class="size-4" /></RouterLink>
                  <button class="icon-btn" title="Edit" @click="editing = i"><Pencil class="size-4" /></button>
                  <button class="icon-btn lg:hidden" title="Channels" @click="channelsFor = i.node_id"><Layers class="size-4" /></button>
                  <button class="icon-btn" title="Show key" @click="keyFor = i"><KeyRound class="size-4" /></button>
                  <button v-if="i.api" class="icon-btn max-sm:hidden" title="Restart API server" :disabled="!i.enabled" @click="restartApi(i)"><RotateCcw class="size-4" /></button>
                  <button v-if="!i.is_relay" class="icon-btn hover:!text-bad" title="Delete" @click="remove(i)"><Trash class="size-4" /></button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-if="!source.length" class="empty">Loading identities…</div>
      </div>
      <div class="border-t border-line-soft px-4 py-2.5 text-xs text-ink-3 sm:px-5">
        Every node number is <span class="mono">crc32(public key)</span>; last bytes must be unique so next-hop routing can tell identities apart.
        “Over share” means an identity is using more than its slice of the hourly duty-cycle budget.
      </div>
    </section>

    <CreateIdentityModal :open="createMode !== null" :mode="createMode ?? 'create'" @close="createMode = null" />
    <EditIdentityModal :identity="editing" @close="editing = null" />
    <KeyModal :identity="keyFor" @close="keyFor = null" />
    <ChannelsDrawer :identity-id="channelsFor" @close="channelsFor = null" />
  </div>
</template>
