<script setup lang="ts">
// The settings form a plugin describes in its plugin.yaml.
import { computed, ref, watch } from 'vue'
import { api, enc } from '@/api/client'
import type { Plugin } from '@/api/types'
import Toggle from '@/components/ui/Toggle.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { toast } from '@/composables/toast'

const MASK = '••••••••'
const props = defineProps<{ plugin: Plugin }>()
const emit = defineEmits<{ saved: [plugin: Plugin] }>()

const form = ref<Record<string, unknown>>({})
const saving = ref(false)
const error = ref('')

function reset() {
  const out: Record<string, unknown> = {}
  for (const s of props.plugin.settings) {
    if (s.type === 'secret') out[s.key] = props.plugin.secrets_set.includes(s.key) ? MASK : ''
    else out[s.key] = props.plugin.values[s.key] ?? (s.type === 'bool' ? false : '')
  }
  form.value = out
  error.value = ''
}
watch(() => props.plugin.id, reset, { immediate: true })

const dirty = computed(() => {
  for (const s of props.plugin.settings) {
    const saved = s.type === 'secret' ? (props.plugin.secrets_set.includes(s.key) ? MASK : '') : (props.plugin.values[s.key] ?? (s.type === 'bool' ? false : ''))
    if (form.value[s.key] !== saved) return true
  }
  return false
})

async function save() {
  saving.value = true
  error.value = ''
  const body: Record<string, unknown> = {}
  for (const s of props.plugin.settings) {
    let v = form.value[s.key]
    if ((s.type === 'int' || s.type === 'number') && v !== '' && v !== null) v = Number(v)
    if (v === '' && s.type !== 'secret' && s.type !== 'string' && s.type !== 'url' && s.type !== 'select') v = null
    body[s.key] = v
  }
  try {
    const p = await api.put<Plugin>(`/plugins/${enc(props.plugin.id)}/settings`, body)
    toast('Settings saved')
    emit('saved', p)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="max-w-2xl">
    <p v-if="plugin.pinned" class="mb-4 rounded-xl bg-info/10 px-3 py-2.5 text-[13px] text-info">
      These settings are set in the config file (<span class="mono">plugins.entries</span>) and can't be changed here.
    </p>
    <p v-if="!plugin.settings.length" class="text-[13px] text-ink-3">
      {{ plugin.kind === 'attached' && !plugin.version ? "The plugin hasn't connected yet, so its settings aren't known." : 'This plugin has no settings.' }}
    </p>
    <form v-else class="grid gap-4 sm:grid-cols-2" @submit.prevent="save">
      <div v-for="s in plugin.settings" :key="s.key" :class="s.type === 'bool' ? 'sm:col-span-2' : s.type === 'url' || s.type === 'secret' || (s.help?.length ?? 0) > 60 ? 'sm:col-span-2' : ''">
        <div v-if="s.type === 'bool'" class="flex items-start justify-between gap-4 rounded-xl border border-line-soft px-3.5 py-3">
          <div>
            <label class="text-[13px] font-medium" :for="`ps-${s.key}`">{{ s.label }}</label>
            <p v-if="s.help" class="hint !mt-0.5">{{ s.help }}</p>
          </div>
          <Toggle :id="`ps-${s.key}`" v-model="form[s.key] as boolean" :disabled="plugin.pinned" :label="s.label" />
        </div>
        <template v-else>
          <label class="label" :for="`ps-${s.key}`">{{ s.label }}<span v-if="s.required" class="text-bad"> *</span></label>
          <select v-if="s.type === 'select'" :id="`ps-${s.key}`" v-model="form[s.key]" class="input" :disabled="plugin.pinned">
            <option v-if="!s.required" value="">—</option>
            <option v-for="o in s.options" :key="o" :value="o">{{ o }}</option>
          </select>
          <input
            v-else
            :id="`ps-${s.key}`"
            v-model="form[s.key]"
            :type="s.type === 'secret' ? 'password' : s.type === 'int' || s.type === 'number' ? 'number' : s.type === 'url' ? 'url' : 'text'"
            :step="s.type === 'int' ? 1 : s.type === 'number' ? 'any' : undefined"
            :class="['input', s.type === 'url' || s.type === 'secret' ? 'mono' : '', s.type === 'int' || s.type === 'number' ? 'tabular-nums' : '']"
            :placeholder="s.placeholder ?? (s.default !== undefined ? String(s.default) : '')"
            :autocomplete="s.type === 'secret' ? 'new-password' : 'off'"
            :disabled="plugin.pinned"
            @focus="s.type === 'secret' && form[s.key] === MASK && (form[s.key] = '')"
            @blur="s.type === 'secret' && form[s.key] === '' && plugin.secrets_set.includes(s.key) && (form[s.key] = MASK)"
          />
          <p v-if="s.help" class="hint">{{ s.help }}</p>
          <p v-if="s.type === 'secret' && plugin.secrets_set.includes(s.key)" class="hint">Saved. Type a new value to replace it.</p>
        </template>
      </div>
      <div v-if="!plugin.pinned" class="flex items-center justify-end gap-2 sm:col-span-2">
        <p v-if="error" class="mr-auto text-[13px] text-bad">{{ error }}</p>
        <button type="button" class="btn" :disabled="!dirty || saving" @click="reset">Discard</button>
        <button class="btn btn-primary" :disabled="!dirty || saving"><Spinner v-if="saving" />Save settings</button>
      </div>
    </form>
  </div>
</template>
