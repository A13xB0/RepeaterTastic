<script setup lang="ts">
// One radio in the top bar: connection, preset and frequency, airtime against its duty cycle, and
// its relay mode.
import { computed } from 'vue'
import type { RelayRole, Status } from '@/api/types'
import RelayModeMenu from '@/components/layout/RelayModeMenu.vue'
import { num } from '@/lib/format'

const props = defineProps<{ status: Status; shared: string[]; busy?: boolean }>()
const emit = defineEmits<{ role: [role: RelayRole] }>()

const st = computed(() => props.status)
const gauge = computed(() => {
  const ratio = st.value.airtime.tx_pct / (st.value.airtime.duty_limit_pct || 100)
  return { pct: Math.min(100, ratio * 100), cls: ratio > 0.9 ? 'bg-bad' : ratio > 0.7 ? 'bg-warn' : 'bg-brand' }
})
const name = computed(() => st.value.radio_name ?? st.value.radio_id ?? 'Main')
</script>

<template>
  <div class="w-64 shrink-0 rounded-xl border border-line-soft bg-raised px-3 py-2">
    <div class="flex items-center gap-2">
      <span
        :class="['dot size-2 shrink-0', st.radio.connected ? 'pulse-dot bg-ok text-ok' : 'bg-bad']"
        :title="`${st.radio.name || 'Modem'} ${st.radio.connected ? 'connected' : 'disconnected'} · ${st.radio.driver} ${st.radio.device} · rx ${st.radio.rx} · tx ${st.radio.tx}`"
      />
      <span class="min-w-0 truncate text-[13px] font-semibold">{{ name }}</span>
      <span
        v-if="shared.length"
        class="chip shrink-0 bg-warn/15 !px-1.5 text-warn"
        :title="`Shares its channel with ${shared.join(', ')}: these radios take turns to transmit`"
      >shared</span>
      <RelayModeMenu class="ml-auto" :role="st.relay.role" :label="name" :disabled="busy" @pick="emit('role', $event)" />
    </div>
    <div
      class="mt-0.5 truncate text-2xs text-ink-3"
      :title="`${st.phy.region} · BW ${st.phy.bw_khz} kHz · SF${st.phy.sf} · CR 4/${st.phy.cr} · ${st.phy.tx_power_dbm} dBm`"
    >
      {{ st.phy.preset_name }} · <span class="tabular-nums">{{ st.phy.frequency_mhz.toFixed(3) }} MHz</span> · {{ st.phy.tx_power_dbm }} dBm
    </div>
    <div class="mt-1.5 flex items-center gap-2" :title="`TX airtime in the last ${st.airtime.window_s / 60} min against the duty cycle`">
      <div class="h-1 flex-1 overflow-hidden rounded-full bg-ink-3/15">
        <div :class="['h-full rounded-full transition-all duration-500', gauge.cls]" :style="{ width: `${Math.max(gauge.pct, 1.5)}%` }" />
      </div>
      <span class="shrink-0 text-2xs tabular-nums text-ink-3"><span class="font-semibold text-ink">{{ st.airtime.tx_pct.toFixed(1) }}</span> / {{ num(st.airtime.duty_limit_pct) }} % airtime</span>
    </div>
  </div>
</template>
