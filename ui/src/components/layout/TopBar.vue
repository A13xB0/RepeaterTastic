<script setup lang="ts">
// After openHop's TopBar (MIT, © Lloyd Newton): radio health, PHY, airtime gauge and relay mode.
import { computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Menu, Monitor, Moon, Sun } from '@lucide/vue'
import { api, radio, setRadio } from '@/api/client'
import type { RelayRole, Status } from '@/api/types'
import { live } from '@/store/live'
import AccountMenu from '@/components/layout/AccountMenu.vue'
import { cycleTheme, themeMode } from '@/composables/theme'
import { toast, toastError } from '@/composables/toast'
import { confirmDialog } from '@/composables/confirm'
import { relayModes } from '@/lib/relay'
import { num } from '@/lib/format'

const emit = defineEmits<{ menu: [] }>()
const route = useRoute()
const s = computed(() => live.status)
const busy = ref(false)

async function setRole(role: RelayRole) {
  if (!s.value || s.value.relay.role === role || busy.value) return
  if (role === 'off' || role === 'monitor') {
    const off = role === 'off'
    const ok = await confirmDialog({
      title: off ? 'Turn the radio off?' : 'Listen only?',
      body: off
        ? 'RepeaterTastic ignores the radio: nothing is received, relayed or sent, and queued messages fail. Identities and links stay up.'
        : 'The radio keeps receiving, but nothing is transmitted: no relaying, and messages from identities and apps fail. Queued messages fail now.',
      confirm: off ? 'Turn off' : 'Listen only',
      danger: off,
    })
    if (!ok || !s.value) return
  }
  busy.value = true
  const prev = s.value.relay.role
  s.value.relay.role = role
  try {
    const relay = await api.put<Status['relay']>('/relay', { role })
    if (live.status) live.status.relay = relay
    if (role === 'off') toast('Radio off')
    else if (role === 'monitor') toast('Listening only')
  } catch (e) {
    if (live.status) live.status.relay.role = prev
    toastError(e)
  } finally {
    busy.value = false
  }
}

const gauge = computed(() => {
  if (!s.value) return { pct: 0, cls: 'bg-brand' }
  const ratio = s.value.airtime.tx_pct / (s.value.airtime.duty_limit_pct || 100)
  return { pct: Math.min(100, ratio * 100), cls: ratio > 0.9 ? 'bg-bad' : ratio > 0.7 ? 'bg-warn' : 'bg-brand' }
})
const overlaps = computed(() => live.radios.find((r) => r.id === radio.value)?.overlaps ?? [])
const syncHex = computed(() => (s.value ? '0x' + s.value.phy.sync_word.toString(16).toUpperCase().padStart(2, '0') : ''))
</script>

<template>
  <header class="card mb-4 flex flex-wrap items-center gap-x-4 gap-y-2.5 px-3 py-2.5 sm:px-4">
    <div class="flex w-full min-w-0 items-center gap-2 lg:hidden">
      <button class="icon-btn lg:hidden" aria-label="Open menu" @click="emit('menu')"><Menu class="size-5" /></button>
      <h1 class="truncate text-[15px] font-semibold tracking-tight">{{ route.meta.title }}</h1>
      <button class="icon-btn ml-auto" :title="`Theme: ${themeMode}`" @click="cycleTheme">
        <Sun v-if="themeMode === 'light'" class="size-4" /><Moon v-else-if="themeMode === 'dark'" class="size-4" /><Monitor v-else class="size-4" />
      </button>
      <AccountMenu />
    </div>

    <div v-if="s" class="flex min-w-0 flex-1 flex-wrap items-center gap-x-5 gap-y-2">
      <div v-if="live.radios.length > 1" class="flex min-w-0 items-center gap-2">
        <label class="eyebrow" for="radio-pick">Radio</label>
        <select id="radio-pick" class="input !h-8 !py-0 text-[13px]" :value="radio" @change="setRadio(($event.target as HTMLSelectElement).value)">
          <option v-for="r in live.radios" :key="r.id" :value="r.id">{{ r.name }} · {{ r.phy.preset_name }}</option>
        </select>
        <span
          v-if="overlaps.length"
          class="chip bg-warn/15 text-warn"
          :title="`Shares its channel with ${overlaps.join(', ')}: these radios take turns to transmit`"
        >shared channel</span>
      </div>

      <div v-if="live.radios.length > 1" class="hidden h-7 w-px bg-line-soft sm:block" />
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
        <div class="truncate text-2xs text-ink-3 max-sm:hidden">sync {{ syncHex }} · SF{{ s.phy.sf }} / {{ num(s.phy.bw_khz) }} kHz · {{ s.phy.tx_power_dbm }} dBm</div>
      </div>

      <div class="hidden h-7 w-px bg-line-soft sm:block" />

      <div class="w-32 leading-tight sm:w-40" :title="`TX airtime in the last ${s.airtime.window_s / 60} min against the region duty cycle`">
        <div class="flex items-baseline justify-between text-2xs text-ink-3">
          <span>Airtime 1 h</span>
          <span class="text-[13px] font-semibold tabular-nums text-ink">{{ s.airtime.tx_pct.toFixed(1) }}<span class="text-ink-3"> / {{ num(s.airtime.duty_limit_pct) }} %</span></span>
        </div>
        <div class="mt-1 h-1.5 overflow-hidden rounded-full bg-ink-3/15">
          <div :class="['h-full rounded-full transition-all duration-500', gauge.cls]" :style="{ width: `${Math.max(gauge.pct, 1.5)}%` }" />
        </div>
      </div>

      <div class="ml-auto flex items-center gap-2">
        <span class="eyebrow hidden md:inline">Relay</span>
        <div class="seg" role="group" aria-label="Relay mode">
          <template v-for="(r, i) in relayModes" :key="r.id">
            <span v-if="i === 3" class="mx-0.5 my-1 w-px bg-line" aria-hidden="true" />
            <button :title="r.title" :aria-pressed="s.relay.role === r.id" :disabled="busy" @click="setRole(r.id)">
              <span :class="s.relay.role === r.id ? r.tone : ''">{{ r.label }}</span>
            </button>
          </template>
        </div>
        <button class="icon-btn max-lg:hidden" :title="`Theme: ${themeMode}`" @click="cycleTheme">
          <Sun v-if="themeMode === 'light'" class="size-4" /><Moon v-else-if="themeMode === 'dark'" class="size-4" /><Monitor v-else class="size-4" />
        </button>
        <AccountMenu class="max-lg:hidden" />
      </div>
    </div>
    <div v-else class="h-9 flex-1 animate-pulse rounded-lg bg-sunken" />
  </header>
</template>
