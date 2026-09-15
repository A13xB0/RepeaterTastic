<script setup lang="ts">
// A plugin's own panel in a sandboxed frame. It can't reach the GUI, the API or its token; it
// talks to the page with postMessage:
//   page → panel  {type: "data", data, theme}   the plugin's latest panel data
//   panel → page  {type: "action", name, payload}   passed to the plugin
//   panel → page  {type: "resize", height}
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { api, enc } from '@/api/client'
import type { Plugin } from '@/api/types'
import { toastError } from '@/composables/toast'
import { on } from '@/store/live'
import { isDark } from '@/composables/theme'

const props = defineProps<{ plugin: Plugin }>()
const frame = ref<HTMLIFrameElement | null>(null)
const height = ref(520)
let data: unknown = null

const theme = () => (isDark.value ? 'dark' : 'light')

function post() {
  frame.value?.contentWindow?.postMessage({ type: 'data', data, theme: theme() }, '*')
}

async function refresh() {
  try {
    data = await api.get(`/plugins/${enc(props.plugin.id)}/panel-data`)
    post()
  } catch {
    /* the plugin may not be connected */
  }
}

async function onMessage(e: MessageEvent) {
  if (!frame.value || e.source !== frame.value.contentWindow) return
  const m = e.data as { type?: string; name?: string; payload?: unknown; height?: number }
  if (m?.type === 'resize' && typeof m.height === 'number') height.value = Math.min(Math.max(m.height, 120), 4000)
  if (m?.type === 'action' && typeof m.name === 'string') {
    try {
      await api.post(`/plugins/${enc(props.plugin.id)}/panel-action`, { name: m.name, payload: m.payload ?? null })
    } catch (err) {
      toastError(err)
    }
  }
  if (m?.type === 'ready') post()
}

let timer: number | undefined
const off = on('plugin', (p) => {
  if (p.id !== props.plugin.id) return
  clearTimeout(timer)
  timer = window.setTimeout(refresh, 250)
})
watch(() => props.plugin.panel_url, refresh)
watch(isDark, post)
onMounted(() => {
  window.addEventListener('message', onMessage)
  refresh()
})
onBeforeUnmount(() => {
  window.removeEventListener('message', onMessage)
  clearTimeout(timer)
  off()
})
</script>

<template>
  <iframe
    v-if="plugin.panel_url"
    ref="frame"
    :src="plugin.panel_url"
    sandbox="allow-scripts allow-popups"
    referrerpolicy="no-referrer"
    :title="`${plugin.name} panel`"
    class="block w-full rounded-xl border border-line-soft bg-surface-solid"
    :style="{ height: `${height}px` }"
    @load="post"
  />
</template>
