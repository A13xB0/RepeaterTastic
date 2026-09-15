import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './main.css'
import './composables/theme'

// A tab left open across a daemon update still runs the old build, whose page chunks are gone.
// Reload into the new build instead of leaving navigation dead (at most once a minute, so a
// genuinely missing file can't loop).
function reloadForNewBuild(to?: string) {
  const KEY = 'rt-build-reload'
  try {
    const last = Number(sessionStorage.getItem(KEY) || 0)
    if (Date.now() - last < 60_000) return false
    sessionStorage.setItem(KEY, String(Date.now()))
  } catch {
    /* storage unavailable: reload anyway */
  }
  if (to) window.location.assign(to)
  else window.location.reload()
  return true
}
window.addEventListener('vite:preloadError', (e) => {
  if (reloadForNewBuild()) e.preventDefault()
})
router.onError((err, to) => {
  if (/dynamically imported module|Importing a module script failed|error loading dynamically imported|MIME type/i.test(String(err?.message ?? err)))
    reloadForNewBuild(to.fullPath)
})

createApp(App).use(router).mount('#app')
