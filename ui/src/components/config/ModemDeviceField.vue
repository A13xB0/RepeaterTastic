<script setup lang="ts">
// Where a radio is: a USB serial port running the KISS firmware (driver kiss), a LoRa board the
// experimental spi driver runs itself (driver spi, device a board), or a board running stock
// Meshtastic firmware over USB or the network (driver meshtastic, device a port or host[:port]).
// Shared by Add radio and Edit radio.
import { computed, onMounted, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { Board, SerialPort } from '@/api/types'
import BoardSelect from '@/components/config/BoardSelect.vue'

const props = withDefaults(defineProps<{ id: string; ports: SerialPort[]; label?: string; restartHint?: boolean }>(), {
  label: 'Modem',
  restartHint: false,
})
const device = defineModel<string>({ required: true })
// The driver that goes with the device: kiss, spi or meshtastic.
const driver = defineModel<string>('driver', { default: '' })

type Kind = 'serial' | 'board' | 'node'
type Via = 'usb' | 'net'
function kindOf(drv: string, dev: string): Kind {
  if (drv === 'meshtastic') return 'node'
  if (drv === 'spi') return 'board'
  if (drv === '' && dev && !dev.startsWith('/dev/')) return 'board'
  return 'serial'
}
const kind = ref<Kind>(kindOf(driver.value, device.value))
const serial = ref(kind.value === 'serial' ? device.value : '')
const board = ref(kind.value === 'board' ? device.value : 'auto')
const via = ref<Via>(kind.value === 'node' && !device.value.startsWith('/dev/') ? 'net' : 'usb')
const nodePort = ref(kind.value === 'node' && via.value === 'usb' ? device.value : '')
const host = ref('')
const port = ref(4403)

// host, host:port, [v6]:port or a bare v6 address
function parseAddr(v: string) {
  const m = /^\[([^\]]+)\](?::(\d+))?$/.exec(v) ?? (/^([^:]+)(?::(\d+))?$/.exec(v)) ?? [v, v, '']
  host.value = m[1] ?? ''
  port.value = m[2] ? Number(m[2]) : 4403
}
if (kind.value === 'node' && via.value === 'net') parseAddr(device.value)

watch([driver, device], ([drv, dev]) => {
  kind.value = kindOf(drv, dev)
  if (kind.value === 'board') board.value = dev
  else if (kind.value === 'serial') serial.value = dev
  else if (dev.startsWith('/dev/')) {
    via.value = 'usb'
    nodePort.value = dev
  } else if (dev) {
    via.value = 'net'
    parseAddr(dev)
  }
})

function nodeAddress(): string {
  const h = host.value.trim()
  if (!h) return ''
  const bracketed = h.includes(':') ? `[${h}]` : h
  return port.value === 4403 ? (h.includes(':') ? bracketed : h) : `${bracketed}:${port.value}`
}

function sync() {
  switch (kind.value) {
    case 'board':
      device.value = board.value.trim()
      driver.value = 'spi'
      break
    case 'node':
      device.value = via.value === 'usb' ? nodePort.value.trim() : nodeAddress()
      driver.value = 'meshtastic'
      break
    default:
      device.value = serial.value.trim()
      driver.value = 'kiss'
  }
}
watch([kind, serial, board, via, nodePort, host, port], sync)

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
        <button type="button" :aria-pressed="kind === 'board'" @click="kind = 'board'">Board</button>
        <button type="button" :aria-pressed="kind === 'node'" @click="kind = 'node'">Meshtastic firmware</button>
      </div>
    </div>

    <template v-if="kind === 'serial'">
      <input :id="`${props.id}-serial`" v-model="serial" class="input mono mt-2" :list="`${props.id}-ports`" placeholder="/dev/serial/by-id/…" aria-label="Serial port" />
      <datalist :id="`${props.id}-ports`"><option v-for="p in ports" :key="p.path" :value="p.path">{{ p.description }}</option></datalist>
      <p class="hint">
        A board flashed with the Mesh KISS firmware. Prefer <span class="mono">/dev/serial/by-id/…</span> so the path survives replugging.<template v-if="restartHint"> Changing it needs a restart.</template>
      </p>
    </template>

    <template v-else-if="kind === 'board'">
      <BoardSelect :id="`${props.id}-board`" v-model="board" :boards="boards" class="mt-2" />
      <p class="hint">
        A LoRa board on SPI or a CH341 USB stick that RepeaterTastic drives itself (experimental), with no meshtasticd on it.<template v-if="restartHint"> Changing it needs a restart.</template>
      </p>
    </template>

    <template v-else>
      <div class="mt-2 flex flex-wrap items-center gap-2">
        <div class="seg" role="group" aria-label="How the board is connected">
          <button type="button" :aria-pressed="via === 'usb'" @click="via = 'usb'">USB</button>
          <button type="button" :aria-pressed="via === 'net'" @click="via = 'net'">Network</button>
        </div>
        <input v-if="via === 'usb'" :id="`${props.id}-node-serial`" v-model="nodePort" class="input mono min-w-0 flex-1" :list="`${props.id}-node-ports`" placeholder="/dev/ttyACM0" aria-label="Board serial port" spellcheck="false" />
        <template v-else>
          <input :id="`${props.id}-node-host`" v-model="host" class="input mono min-w-0 flex-1" placeholder="192.168.1.20 or meshtastic.local" aria-label="Board address" spellcheck="false" />
          <input :id="`${props.id}-node-port`" v-model.number="port" type="number" min="1" max="65535" class="input mono w-24" aria-label="Board API port" />
        </template>
      </div>
      <datalist :id="`${props.id}-node-ports`"><option v-for="p in ports" :key="p.path" :value="p.path">{{ p.description }}</option></datalist>
      <p v-if="via === 'net' && !portValid" class="hint !text-bad">Port must be 1–65535.</p>
      <p class="hint">
        A board running stock Meshtastic firmware (Heltec, T-Beam, RAK, T-Echo…). It does the radio work and is this radio's relay; region, preset and role are written to it.
        Identities reach the air through it, one hop behind, over its MQTT client proxy (its own MQTT connection stops).
        <template v-if="restartHint"> Changing it needs a restart.</template>
      </p>
    </template>
  </div>
</template>
