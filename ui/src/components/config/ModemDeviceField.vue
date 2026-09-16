<script setup lang="ts">
// Where a radio's modem is: a USB serial port running the KISS firmware, meshtasticd's raw modem
// over TCP (radio.device tcp://host:port), or a LoRa board the experimental spi driver runs
// itself (radio.driver spi, radio.device a board). Shared by Add radio and Edit radio.
import { computed, onMounted, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { Board, SerialPort } from '@/api/types'
import BoardSelect from '@/components/config/BoardSelect.vue'
import { live } from '@/store/live'

const props = withDefaults(defineProps<{ id: string; ports: SerialPort[]; label?: string; restartHint?: boolean }>(), {
  label: 'Modem',
  restartHint: false,
})
const device = defineModel<string>({ required: true })
// The driver that goes with the device: kiss for a serial port or meshtasticd, spi for a board.
const driver = defineModel<string>('driver', { default: '' })

type Kind = 'serial' | 'tcp' | 'board'
function kindOf(drv: string, dev: string): Kind {
  if (drv === 'spi') return 'board'
  if (dev.startsWith('tcp://')) return 'tcp'
  if (drv === '' && dev && !dev.startsWith('/dev/')) return 'board'
  return 'serial'
}
const kind = ref<Kind>(kindOf(driver.value, device.value))
const host = ref('127.0.0.1')
const port = ref(4405)
const serial = ref(kind.value === 'serial' ? device.value : '')
const board = ref(kind.value === 'board' ? device.value : 'auto')

function parseTCP(v: string) {
  const m = /^tcp:\/\/(\[[^\]]+\]|[^:/]+):(\d+)$/.exec(v)
  if (m) {
    host.value = m[1]
    port.value = Number(m[2])
  }
}
parseTCP(device.value)

watch([driver, device], ([drv, dev]) => {
  kind.value = kindOf(drv, dev)
  if (kind.value === 'tcp') parseTCP(dev)
  else if (kind.value === 'board') board.value = dev
  else serial.value = dev
})

function sync() {
  switch (kind.value) {
    case 'tcp':
      device.value = `tcp://${host.value.trim()}:${port.value}`
      driver.value = 'kiss'
      break
    case 'board':
      device.value = board.value.trim()
      driver.value = 'spi'
      break
    default:
      device.value = serial.value.trim()
      driver.value = 'kiss'
  }
}
watch([kind, host, port, serial, board], sync)

const portValid = computed(() => Number.isInteger(port.value) && port.value >= 1 && port.value <= 65535)

const boards = ref<Board[]>([])
onMounted(async () => {
  try {
    boards.value = await api.get<Board[]>('/boards')
  } catch {
    /* the select still offers auto and a file path */
  }
})
</script>

<template>
  <div>
    <div class="flex flex-wrap items-center justify-between gap-2">
      <span class="label !mb-0">{{ label }}</span>
      <div class="seg" role="group" :aria-label="`${label} connection`">
        <button type="button" :aria-pressed="kind === 'serial'" @click="kind = 'serial'">USB modem</button>
        <button v-if="live.meshtasticdRawModem || kind === 'tcp'" type="button" :aria-pressed="kind === 'tcp'" @click="kind = 'tcp'">meshtasticd</button>
        <button type="button" :aria-pressed="kind === 'board'" @click="kind = 'board'">Board</button>
      </div>
    </div>

    <template v-if="kind === 'serial'">
      <input :id="`${props.id}-serial`" v-model="serial" class="input mono mt-2" :list="`${props.id}-ports`" placeholder="/dev/serial/by-id/…" aria-label="Serial port" />
      <datalist :id="`${props.id}-ports`"><option v-for="p in ports" :key="p.path" :value="p.path">{{ p.description }}</option></datalist>
      <p class="hint">
        A board flashed with the Mesh KISS firmware. Prefer <span class="mono">/dev/serial/by-id/…</span> so the path survives replugging.<template v-if="restartHint"> Changing it needs a restart.</template>
      </p>
    </template>

    <template v-else-if="kind === 'tcp'">
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

    <template v-else>
      <BoardSelect :id="`${props.id}-board`" v-model="board" :boards="boards" class="mt-2" />
      <p class="hint">
        A LoRa board on SPI or a CH341 USB stick that RepeaterTastic drives itself (experimental), with no meshtasticd on it.<template v-if="restartHint"> Changing it needs a restart.</template>
      </p>
    </template>
  </div>
</template>
