<script setup lang="ts">
// Layout after openHop's DashboardLayout (MIT, © Lloyd Newton): floating sidebar card + top bar + scrolling content.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import Sidebar from './Sidebar.vue'
import TopBar from './TopBar.vue'
import { startLive, stopLive } from '@/store/live'
import { token } from '@/api/client'

const drawer = ref(false)
const route = useRoute()
watch(() => route.fullPath, () => (drawer.value = false))

onMounted(() => startLive())
watch(token, (t) => !t && stopLive())
onBeforeUnmount(() => stopLive())
</script>

<template>
  <div class="app-bg flex h-dvh overflow-hidden">
    <Sidebar :open="drawer" @close="drawer = false" />
    <main class="min-w-0 flex-1 overflow-y-auto overscroll-contain px-3 pb-6 pt-3 sm:px-4 lg:py-[14px] lg:pl-0 lg:pr-[14px]">
      <TopBar @menu="drawer = true" />
      <RouterView v-slot="{ Component }">
        <component :is="Component" />
      </RouterView>
    </main>
  </div>
</template>
