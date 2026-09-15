<script setup lang="ts">
// Install a plugin: upload a bundle, give a URL, or see how to install without the GUI.
import { ref, watch } from 'vue'
import { FileArchive, Upload } from '@lucide/vue'
import { api, upload } from '@/api/client'
import type { Plugin } from '@/api/types'
import Modal from '@/components/ui/Modal.vue'
import Spinner from '@/components/ui/Spinner.vue'
import CopyButton from '@/components/ui/CopyButton.vue'

const props = defineProps<{ open: boolean; allowUrl: boolean; folder?: string }>()
const emit = defineEmits<{ close: []; installed: [plugin: Plugin] }>()

const mode = ref<'upload' | 'url' | 'manual'>('upload')
const file = ref<File | null>(null)
const url = ref('')
const busy = ref(false)
const error = ref('')
const dragging = ref(false)

watch(
  () => props.open,
  (o) => {
    if (o) {
      file.value = null
      url.value = ''
      error.value = ''
      mode.value = 'upload'
    }
  },
)

function pick(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (f) file.value = f
}
function drop(e: DragEvent) {
  dragging.value = false
  const f = e.dataTransfer?.files?.[0]
  if (f) file.value = f
}

async function install() {
  busy.value = true
  error.value = ''
  try {
    const p = mode.value === 'url' ? await api.post<Plugin>('/plugins', { url: url.value.trim() }) : await upload<Plugin>('/plugins', 'bundle', file.value!)
    emit('installed', p)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

const mb = (n: number) => (n / 1048576).toFixed(n < 1048576 ? 2 : 1)
</script>

<template>
  <Modal :open="open" title="Install a plugin" subtitle="A plugin bundle is a .zip with a plugin.yaml. It is installed switched off." size="lg" @close="emit('close')">
    <div class="tabs -mx-5 -mt-4 mb-4 px-4" role="tablist">
      <button role="tab" :aria-selected="mode === 'upload'" @click="mode = 'upload'">Upload</button>
      <button v-if="allowUrl" role="tab" :aria-selected="mode === 'url'" @click="mode = 'url'">From a URL</button>
      <button role="tab" :aria-selected="mode === 'manual'" @click="mode = 'manual'">Without the GUI</button>
    </div>

    <label
      v-if="mode === 'upload'"
      for="pl-file"
      :class="[
        'flex cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border-2 border-dashed px-4 py-10 text-center transition-colors',
        dragging ? 'border-brand bg-brand/5' : 'border-line hover:border-ink-3',
      ]"
      @dragover.prevent="dragging = true"
      @dragleave="dragging = false"
      @drop.prevent="drop"
    >
      <template v-if="file">
        <FileArchive class="size-7 text-brand" />
        <span class="text-[13px] font-medium">{{ file.name }}</span>
        <span class="text-xs text-ink-3">{{ mb(file.size) }} MB · choose another</span>
      </template>
      <template v-else>
        <Upload class="size-7 text-ink-3" />
        <span class="text-[13px] font-medium">Drop a plugin bundle here, or choose a file</span>
        <span class="text-xs text-ink-3">.zip, up to 100 MB</span>
      </template>
      <input id="pl-file" type="file" accept=".zip,application/zip" class="sr-only" @change="pick" />
    </label>

    <div v-else-if="mode === 'url'">
      <label class="label" for="pl-url">Bundle URL</label>
      <input id="pl-url" v-model="url" class="input mono" placeholder="https://github.com/…/releases/download/v1.0.0/plugin.zip" />
      <p class="hint">RepeaterTastic downloads it over the network and checks it before installing. Only use URLs from people you trust.</p>
    </div>

    <div v-else class="grid gap-4 text-[13px]">
      <div>
        <div class="font-medium">Drop it into the plugins folder</div>
        <p class="mt-1 text-ink-3">Copy the .zip into this folder. RepeaterTastic installs it within a few seconds; a bundle it can't use is moved to <span class="mono">.rejected/</span> with the reason.</p>
        <div class="mono mt-2 flex items-center gap-2 rounded-lg bg-raised px-2.5 py-1.5 text-xs">
          <span class="flex-1 truncate">{{ folder || '<state_dir>/plugins/inbox' }}</span><CopyButton v-if="folder" :text="folder" label="Folder" />
        </div>
      </div>
      <div>
        <div class="font-medium">Use the command line</div>
        <pre class="mono mt-2 overflow-x-auto rounded-lg bg-raised px-2.5 py-2 text-xs leading-relaxed">sudo -u repeatertastic repeatertastic plugin install meshflow.zip
sudo -u repeatertastic repeatertastic plugin enable meshflow all
repeatertastic plugin list</pre>
      </div>
      <div>
        <div class="font-medium">Or in Docker</div>
        <pre class="mono mt-2 overflow-x-auto rounded-lg bg-raised px-2.5 py-2 text-xs leading-relaxed">docker cp meshflow.zip repeatertastic:/data/plugins/inbox/</pre>
      </div>
      <p class="text-xs text-ink-3">The config file can also pin a plugin's switch, permissions and settings under <span class="mono">plugins.entries</span>; see docs/plugins.md.</p>
    </div>

    <p v-if="error" class="mt-3 text-[13px] text-bad">{{ error }}</p>
    <template #footer>
      <button class="btn" @click="emit('close')">{{ mode === 'manual' ? 'Close' : 'Cancel' }}</button>
      <button v-if="mode !== 'manual'" class="btn btn-primary" :disabled="busy || (mode === 'upload' ? !file : !url.trim())" @click="install">
        <Spinner v-if="busy" />Install
      </button>
    </template>
  </Modal>
</template>
