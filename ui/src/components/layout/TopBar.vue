<script setup lang="ts">
// After openHop's TopBar (MIT, © Lloyd Newton): radio health, PHY, airtime gauge and relay mode.
// A multi-radio site gets one compact row per radio, each with its own relay mode.
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Menu, Monitor, Moon, Sun } from '@lucide/vue'
import { MAIN_RADIO, api, withRadio } from '@/api/client'
import type { RelayRole, Status } from '@/api/types'
import { live, statusRadio } from '@/store/live'
import AccountMenu from '@/components/layout/AccountMenu.vue'
import NodesHealthChip from '@/components/layout/NodesHealthChip.vue'
import { cycleTheme, themeMode } from '@/composables/theme'
import { toast, toastError } from '@/composables/toast'
import { confirmDialog } from '@/composables/confirm'
import { relayModes } from '@/lib/relay'
import { num } from '@/lib/format'

const emit = defineEmits<{ menu: [] }>()
const route = useRoute()
const s = computed(() => live.status)
const busy = ref<string | null>(null)
const several = computed(() => live.radios.length > 1)
// Every radio's status, in the site's radio order.
const rows = computed(() => live.radios.map((r) => live.statuses[r.id]).filter((x): x is Status => !!x))

async function setRole(st: Status, role: RelayRole) {
  const id = statusRadio(st)
  if (st.relay.role === role || busy.value) return
  if (role === 'off' || role === 'monitor') {
    const off = role === 'off'
    const where = several.value ? ` ${st.radio_name ?? id}` : ''
    const ok = await confirmDialog({
      title: off ? `Turn the radio${where} off?` : `Listen only on${where || ' this radio'}?`,
      body: off
        ? 'RepeaterTastic ignores the radio: nothing is received, relayed or sent, and queued messages fail. Identities and links stay up.'
        : 'The radio keeps receiving, but nothing is transmitted: no relaying, and messages from identities and apps fail. Queued messages fail now.',
      confirm: off ? 'Turn off' : 'Listen only',
      danger: off,
    })
    if (!ok) return
  }
  busy.value = id
  const prev = st.relay.role
  st.relay.role = role
  try {
    const relay = await api.put<Status['relay']>(withRadio('/relay', id), { role })
    const cur = live.statuses[id]
    if (cur) cur.relay = relay
    if (id === MAIN_RADIO && live.status) live.status.relay = relay
    if (role === 'off') toast('Radio off')
    else if (role === 'monitor') toast('Listening only')
  } catch (e) {
    st.relay.role = prev
    toastError(e)
  } finally {
    busy.value = null
  }
}

function gauge(st: Status) {
  const ratio = st.airtime.tx_pct / (st.airtime.duty_limit_pct || 100)
  return { pct: Math.min(100, ratio * 100), cls: ratio > 0.9 ? 'bg-bad' : ratio > 0.7 ? 'bg-warn' : 'bg-brand' }
}
const overlaps = (id: string) => live.radios.find((r) => r.id === id)?.overlaps ?? []
const syncHex = (st: Status) => '0x' + st.phy.sync_word.toString(16).toUpperCase().padStart(2, '0')
</script>

<template>
  <header class="card mb-4 flex flex-wrap items-center gap-x-4 gap-y-2.5 px-3 py-2.5 sm:px-4">
    <div class="flex w-full min-w-0 items-center gap-2 lg:hidden">
      <button type="button" class="icon-btn lg:hidden" aria-label="Open menu" @click="emit('menu')"><Menu class="size-5" /></button>
      <h1 class="truncate text-[15px] font-semibold tracking-tight">{{ route.meta.title }}</h1>
      <button type="button" class="icon-btn ml-auto" :title="`Theme: ${themeMode}`" @click="cycleTheme">
        <Sun v-if="themeMode === 'light'" class="size-4" /><Moon v-else-if="themeMode === 'dark'" class="size-4" /><Monitor v-else class="size-4" />
      </button>
      <AccountMenu />
    </div>

    <!-- One radio: its health, PHY, meshtasticd, airtime and relay mode on one line. -->
    <div v-if="s && !several" class="flex min-w-0 flex-1 flex-wrap items-center gap-x-5 gap-y-2">
      <div class="flex min-w-0 items-center gap-2.5" :title="`${s.radio.firmware} · rx ${s.radio.rx} · tx ${s.radio.tx} · errors ${s.radio.errors} · reconnects ${s.radio.reconnects}`">
        <span :class="['dot size-2.5', s.radio.connected ? 'pulse-dot bg-ok text-ok' : 'bg-bad']" />
        <div class="min-w-0 leading-tight">
          <div class="truncate text-[13px] font-semibold">{{ s.radio.name || 'Modem' }} <span class="font-normal text-ink-3">· {{ s.radio.connected ? 'connected' : 'disconnected' }}</span></div>
          <div class="mono truncate text-2xs text-ink-3 max-sm:hidden">{{ s.radio.driver }} · {{ s.radio.device }}</div>
        </div>
      </div>

      <div class="hidden h-7 w-px bg-line-soft sm:block" />

      <div class="min-w-0 leading-tight" :title="`BW ${s.phy.bw_khz} kHz · SF${s.phy.sf} · CR 4/${s.phy.cr} · slot ${s.phy.slot + 1}/${s.phy.num_slots} · preamble ${s.phy.preamble} · ${s.phy.tx_power_dbm} dBm`">
        <div class="truncate text-[13px] font-semibold">
          {{ s.phy.region }} <span class="text-ink-3">·</span> {{ s.phy.preset_name }} <span class="text-ink-3">·</span>
          <span class="tabular-nums">{{ s.phy.frequency_mhz.toFixed(3) }} MHz</span>
        </div>
        <div class="truncate text-2xs text-ink-3 max-sm:hidden">sync {{ syncHex(s) }} · SF{{ s.phy.sf }} / {{ num(s.phy.bw_khz) }} kHz · {{ s.phy.tx_power_dbm }} dBm</div>
      </div>

      <div class="hidden h-7 w-px bg-line-soft sm:block" />

      <RouterLink v-if="s.nodes" :to="{ name: 'config', params: { tab: 'meshtasticd' } }" class="min-w-0 leading-tight" aria-label="meshtasticd status">
        <div class="text-2xs text-ink-3 max-sm:hidden">meshtasticd</div>
        <NodesHealthChip :health="s.nodes" compact />
      </RouterLink>

      <div v-if="s.nodes" class="hidden h-7 w-px bg-line-soft sm:block" />

      <div class="w-32 leading-tight sm:w-40" :title="`TX airtime in the last ${s.airtime.window_s / 60} min against the region duty cycle`">
        <div class="flex items-baseline justify-between text-2xs text-ink-3">
          <span>Airtime 1 h</span>
          <span class="text-[13px] font-semibold tabular-nums text-ink">{{ s.airtime.tx_pct.toFixed(1) }}<span class="text-ink-3"> / {{ num(s.airtime.duty_limit_pct) }} %</span></span>
        </div>
        <div class="mt-1 h-1.5 overflow-hidden rounded-full bg-ink-3/15">
          <div :class="['h-full rounded-full transition-all duration-500', gauge(s).cls]" :style="{ width: `${Math.max(gauge(s).pct, 1.5)}%` }" />
        </div>
      </div>

      <div class="ml-auto flex items-center gap-2">
        <span class="eyebrow hidden md:inline">Relay</span>
        <div class="seg" role="group" aria-label="Relay mode">
          <template v-for="(r, i) in relayModes" :key="r.id">
            <span v-if="i > 0 && relayModes[i - 1]!.group !== r.group" class="mx-0.5 my-1 w-px bg-line" aria-hidden="true" />
            <button type="button" :title="r.title" :aria-pressed="s.relay.role === r.id" :disabled="!!busy" @click="setRole(s, r.id)">
              <span :class="s.relay.role === r.id ? r.tone : ''">{{ r.label }}</span>
            </button>
          </template>
        </div>
        <button type="button" class="icon-btn max-lg:hidden" :title="`Theme: ${themeMode}`" @click="cycleTheme">
          <Sun v-if="themeMode === 'light'" class="size-4" /><Moon v-else-if="themeMode === 'dark'" class="size-4" /><Monitor v-else class="size-4" />
        </button>
        <AccountMenu class="max-lg:hidden" />
      </div>
    </div>

    <!-- Several radios: one row each, then the site's meshtasticd health. -->
    <div v-else-if="s" class="flex min-w-0 flex-1 flex-wrap items-start gap-x-5 gap-y-2">
      <div class="scroll-thin min-w-0 flex-1 overflow-x-auto">
        <table class="w-full border-separate border-spacing-x-0 border-spacing-y-1 text-[13px]">
          <caption class="sr-only">Radios</caption>
          <tbody>
            <tr v-for="st in rows" :key="statusRadio(st)" class="align-middle">
              <th scope="row" class="whitespace-nowrap pr-4 text-left font-semibold" :title="`${st.radio.name} · ${st.radio.driver} ${st.radio.device} · rx ${st.radio.rx} · tx ${st.radio.tx}`">
                <span :class="['dot mr-2 inline-block size-2 align-middle', st.radio.connected ? 'bg-ok' : 'bg-bad']" :aria-label="st.radio.connected ? 'connected' : 'disconnected'" />{{ st.radio_name }}
                <span v-if="overlaps(statusRadio(st)).length" class="chip ml-1 bg-warn/15 text-warn" :title="`Shares its channel with ${overlaps(statusRadio(st)).join(', ')}: these radios take turns to transmit`">shared</span>
              </th>
              <td class="whitespace-nowrap pr-4 text-ink-2" :title="`sync ${syncHex(st)} · SF${st.phy.sf} / ${num(st.phy.bw_khz)} kHz · ${st.phy.tx_power_dbm} dBm`">
                {{ st.phy.preset_name }} <span class="text-ink-3">·</span> <span class="tabular-nums">{{ st.phy.frequency_mhz.toFixed(3) }} MHz</span>
              </td>
              <td class="w-36 pr-4" :title="`TX airtime in the last ${st.airtime.window_s / 60} min against the duty cycle`">
                <div class="flex items-baseline justify-between gap-2 text-2xs text-ink-3">
                  <span>Airtime</span>
                  <span class="tabular-nums"><span class="font-semibold text-ink">{{ st.airtime.tx_pct.toFixed(1) }}</span> / {{ num(st.airtime.duty_limit_pct) }} %</span>
                </div>
                <div class="mt-0.5 h-1 overflow-hidden rounded-full bg-ink-3/15">
                  <div :class="['h-full rounded-full transition-all duration-500', gauge(st).cls]" :style="{ width: `${Math.max(gauge(st).pct, 1.5)}%` }" />
                </div>
              </td>
              <td class="whitespace-nowrap">
                <label class="sr-only" :for="`relay-${statusRadio(st)}`">Relay mode on {{ st.radio_name }}</label>
                <select
                  :id="`relay-${statusRadio(st)}`"
                  class="input !h-7 !w-auto !py-0 text-xs"
                  :class="relayModes.find((m) => m.id === st.relay.role)?.tone"
                  :value="st.relay.role"
                  :disabled="!!busy"
                  @change="setRole(st, ($event.target as HTMLSelectElement).value as RelayRole)"
                >
                  <option v-for="m in relayModes" :key="m.id" :value="m.id" :title="m.title">{{ m.label }}</option>
                </select>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="flex items-center gap-3 self-center">
        <RouterLink v-if="s.nodes" :to="{ name: 'config', params: { tab: 'meshtasticd' } }" class="leading-tight" aria-label="meshtasticd status">
          <div class="text-2xs text-ink-3 max-sm:hidden">meshtasticd</div>
          <NodesHealthChip :health="s.nodes" compact />
        </RouterLink>
        <button type="button" class="icon-btn max-lg:hidden" :title="`Theme: ${themeMode}`" @click="cycleTheme">
          <Sun v-if="themeMode === 'light'" class="size-4" /><Moon v-else-if="themeMode === 'dark'" class="size-4" /><Monitor v-else class="size-4" />
        </button>
        <AccountMenu class="max-lg:hidden" />
      </div>
    </div>
    <div v-else class="h-9 flex-1 animate-pulse rounded-lg bg-sunken" />
  </header>
</template>
