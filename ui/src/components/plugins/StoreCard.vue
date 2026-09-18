<script setup lang="ts">
// One plugin as the store offers it. What it will ask for is on the card, before Install, so the
// permissions aren't a surprise after the download.
import { computed } from 'vue'
import { ArrowUpCircle, Check, Container, Globe, Puzzle, Radio } from '@lucide/vue'
import type { StorePlugin } from '@/api/types'
import Spinner from '@/components/ui/Spinner.vue'
import { transmits } from './PluginBits'

const props = defineProps<{ plugin: StorePlugin; permissions: Record<string, string>; busy?: boolean }>()
const emit = defineEmits<{ install: [plugin: StorePlugin] }>()

const p = computed(() => props.plugin)
const sends = computed(() => p.value.permissions.some(transmits))
const size = computed(() => {
  const mb = p.value.latest.size / 1_000_000
  return mb >= 10 ? `${Math.round(mb)} MB` : `${mb.toFixed(1)} MB`
})
// An attached plugin runs in its own container, so there is nothing for this node to install.
const attachOnly = computed(() => !!p.value.image)
</script>

<template>
  <section class="card flex flex-col p-4 sm:p-5">
    <div class="flex items-start gap-3">
      <span class="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-xl border border-line-soft bg-raised text-ink-3">
        <img v-if="p.logo_url" :src="p.logo_url" alt="" class="size-full object-contain p-1" />
        <Puzzle v-else class="size-5" />
      </span>
      <div class="min-w-0 flex-1">
        <a :href="p.homepage" target="_blank" rel="noopener noreferrer" class="block truncate text-[14px] font-semibold hover:underline">{{ p.name }}</a>
        <div class="truncate text-xs text-ink-3">
          v{{ p.latest.version }}<template v-if="p.author"> · {{ p.author }}</template> · {{ size }}
        </div>
      </div>
      <span v-if="p.update_available" class="chip bg-info/12 text-info"><ArrowUpCircle class="size-3" />update</span>
      <span v-else-if="p.installed" class="chip bg-ok/14 text-ok"><Check class="size-3" />installed</span>
    </div>

    <p class="mt-3 line-clamp-2 text-xs leading-relaxed text-ink-2">{{ p.summary }}</p>

    <div class="mt-3 flex flex-wrap gap-1.5">
      <span v-for="key in p.permissions" :key="key" class="chip bg-sunken text-ink-2" :title="permissions[key] ?? key">
        <Radio v-if="transmits(key)" class="size-3 text-warn" />{{ key }}
      </span>
      <span v-if="!p.permissions.length" class="chip bg-sunken text-ink-3">asks for nothing</span>
    </div>

    <p v-if="p.network?.length" class="mt-2 flex items-start gap-1.5 text-xs text-ink-3">
      <Globe class="mt-0.5 size-3.5 shrink-0" />Talks to {{ p.network.join(', ') }}
    </p>
    <p v-if="sends" class="mt-2 text-xs text-warn">Sends on the radio, within your send limits.</p>

    <p v-if="p.update_available" class="mt-3 text-xs text-ink-2">
      You have v{{ p.installed }}.<template v-if="p.latest.notes"> {{ p.latest.notes }}</template>
    </p>

    <div class="mt-auto flex items-center justify-end gap-2 pt-4">
      <span v-if="attachOnly" class="mr-auto flex items-center gap-1.5 text-xs text-ink-3"><Container class="size-3.5" />runs in its own container</span>
      <span v-else-if="p.unusable" class="mr-auto text-xs text-ink-3">{{ p.unusable }}</span>
      <a v-if="attachOnly" class="btn btn-sm" :href="p.homepage" target="_blank" rel="noopener noreferrer">How to attach</a>
      <button v-else type="button" class="btn btn-sm" :class="p.update_available ? 'btn-primary' : ''" :disabled="busy || !!p.unusable || (!!p.installed && !p.update_available)" @click="emit('install', p)">
        <Spinner v-if="busy" />
        {{ p.update_available ? 'Update' : p.installed ? 'Installed' : 'Install' }}
      </button>
    </div>
  </section>
</template>
