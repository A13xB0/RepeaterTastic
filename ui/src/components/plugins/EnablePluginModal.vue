<script setup lang="ts">
// Turning a plugin on: say plainly what it may do, and let the operator hold back permissions.
import { computed, ref, watch } from 'vue'
import { Globe, Radio, TriangleAlert } from '@lucide/vue'
import { api, enc } from '@/api/client'
import type { Plugin } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { toast } from '@/composables/toast'
import { transmits } from './PluginBits'

const props = defineProps<{ plugin: Plugin | null; messagesPerHour?: number; traceroutesPerHour?: number }>()
const emit = defineEmits<{ close: []; done: [plugin: Plugin]; settings: [] }>()

const granted = ref<string[]>([])
const busy = ref(false)
const error = ref('')

watch(
  () => props.plugin?.id,
  () => {
    error.value = ''
    granted.value = props.plugin?.permissions.map((p) => p.key) ?? []
  },
  { immediate: true },
)

const missing = computed(() =>
  (props.plugin?.settings ?? []).filter((s) => {
    if (!s.required) return false
    if (s.type === 'secret') return !props.plugin!.secrets_set.includes(s.key)
    const v = props.plugin!.values[s.key]
    return v === undefined || v === null || v === ''
  }),
)
const sends = computed(() => granted.value.some(transmits))

async function enable() {
  if (!props.plugin) return
  busy.value = true
  error.value = ''
  try {
    const p = await api.post<Plugin>(`/plugins/${enc(props.plugin.id)}/enable`, { permissions: granted.value })
    toast(`${p.name} enabled`)
    emit('done', p)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="!!plugin" :title="`Enable ${plugin?.name ?? ''}`" subtitle="The plugin runs as its own program on this host." size="md" @close="emit('close')">
    <template v-if="plugin">
      <div v-if="missing.length" class="mb-4 flex items-start gap-2.5 rounded-xl bg-warn/10 px-3 py-2.5 text-[13px] text-warn">
        <TriangleAlert class="mt-0.5 size-4 shrink-0" />
        <div>
          Fill in {{ missing.map((s) => s.label).join(', ') }} before turning it on.
          <button class="mt-1 block font-medium underline underline-offset-2" @click="emit('settings')">Open settings</button>
        </div>
      </div>

      <div class="eyebrow mb-2">It may</div>
      <p v-if="!plugin.permissions.length" class="text-[13px] text-ink-3">This plugin asks for no permissions. It can report its status and write to its log.</p>
      <label v-for="p in plugin.permissions" :key="p.key" class="flex cursor-pointer items-start gap-3 border-b border-line-soft py-2.5 last:border-0">
        <input v-model="granted" type="checkbox" :value="p.key" class="mt-0.5 size-4 accent-[var(--brand)]" />
        <span class="min-w-0 flex-1">
          <span class="block text-[13px]">{{ p.text }}</span>
          <span class="mono text-2xs text-ink-3">{{ p.key }}</span>
        </span>
        <Radio v-if="transmits(p.key)" class="mt-0.5 size-4 shrink-0 text-warn" aria-label="Transmits" />
      </label>
      <p v-if="sends" class="mt-2 text-xs text-ink-3">
        Transmissions use the radio's airtime and are capped at {{ messagesPerHour ?? 0 }} messages and {{ traceroutesPerHour ?? 0 }} traceroutes an hour
        (change them under Plugins → Send limits).
      </p>

      <div v-if="plugin.network?.length" class="mt-4 flex items-start gap-2.5 rounded-xl bg-raised px-3 py-2.5 text-[13px]">
        <Globe class="mt-0.5 size-4 shrink-0 text-ink-3" />
        <div>
          Talks to <span v-for="(h, i) in plugin.network" :key="h"><span class="mono">{{ h }}</span>{{ i < plugin.network.length - 1 ? ', ' : '' }}</span>
          <div class="text-xs text-ink-3">Anything the plugin can see, it can send to these services.</div>
        </div>
      </div>
      <p class="mt-4 text-xs text-ink-3">Unticked permissions are refused when the plugin asks. Some plugins stop working without them.</p>
      <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>
    </template>
    <template #footer>
      <button class="btn" @click="emit('close')">Cancel</button>
      <button class="btn btn-primary" :disabled="busy || missing.length > 0" @click="enable"><Spinner v-if="busy" />Enable</button>
    </template>
  </Modal>
</template>
