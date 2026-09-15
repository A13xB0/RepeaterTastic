<script setup lang="ts">
// Where a radio's KISS modem is: a USB serial port, or meshtasticd's raw modem over TCP
// (radio.device tcp://host:port). Shared by the setup wizard, Add radio and Edit radio.
import { computed, ref, watch } from 'vue'
import type { SerialPort } from '@/api/types'

const props = withDefaults(defineProps<{ id: string; ports: SerialPort[]; label?: string; restartHint?: boolean }>(), {
  label: 'Modem',
  restartHint: false,
})
const device = defineModel<string>({ required: true })

type Kind = 'serial' | 'tcp'
const kind = ref<Kind>(device.value.startsWith('tcp://') ? 'tcp' : 'serial')
const host = ref('127.0.0.1')
const port = ref(4405)
const serial = ref(device.value.startsWith('tcp://') ? '' : device.value)

function parseTCP(v: string) {
  const m = /^tcp:\/\/(\[[^\]]+\]|[^:/]+):(\d+)$/.exec(v)
  if (m) {
    host.value = m[1]
    port.value = Number(m[2])
  }
}
parseTCP(device.value)

watch(device, (v) => {
  if (v.startsWith('tcp://')) {
    kind.value = 'tcp'
    parseTCP(v)
  } else if (v) {
    kind.value = 'serial'
    serial.value = v
  }
})

function sync() {
  device.value = kind.value === 'tcp' ? `tcp://${host.value.trim()}:${port.value}` : serial.value.trim()
}
watch([kind, host, port, serial], sync)

const portValid = computed(() => Number.isInteger(port.value) && port.value >= 1 && port.value <= 65535)
</script>

<template>
  <div>
    <div class="flex flex-wrap items-center justify-between gap-2">
      <span class="label !mb-0">{{ label }}</span>
      <div class="seg" role="group" :aria-label="`${label} connection`">
        <button type="button" :aria-pressed="kind === 'serial'" @click="kind = 'serial'">USB modem</button>
        <button type="button" :aria-pressed="kind === 'tcp'" @click="kind = 'tcp'">meshtasticd</button>
      </div>
    </div>

    <template v-if="kind === 'serial'">
      <input :id="`${props.id}-serial`" v-model="serial" class="input mono mt-2" :list="`${props.id}-ports`" placeholder="/dev/serial/by-id/…" aria-label="Serial port" />
      <datalist :id="`${props.id}-ports`"><option v-for="p in ports" :key="p.path" :value="p.path">{{ p.description }}</option></datalist>
      <p class="hint">
        A board flashed with the Mesh KISS firmware. Prefer <span class="mono">/dev/serial/by-id/…</span> so the path survives replugging.<template v-if="restartHint"> Changing it needs a restart.</template>
      </p>
    </template>

    <template v-else>
      <div class="mt-2 grid grid-cols-[1fr_7rem] gap-2">
        <input :id="`${props.id}-host`" v-model="host" class="input mono" placeholder="127.0.0.1" aria-label="meshtasticd host" spellcheck="false" />
        <input :id="`${props.id}-port`" v-model.number="port" type="number" min="1" max="65535" class="input mono" aria-label="Raw modem port" />
      </div>
      <p v-if="!portValid" class="hint !text-bad">Port must be 1–65535.</p>
      <p class="hint">
        meshtasticd's LoRa radio (a Pi HAT or USB stick), served with <span class="mono">RawModemPort: {{ port || 4405 }}</span> under
        <span class="mono">General</span> in its config.yaml. meshtasticd stops being a mesh node while RepeaterTastic uses it.<template v-if="restartHint"> Changing it needs a restart.</template>
      </p>
    </template>
  </div>
</template>
