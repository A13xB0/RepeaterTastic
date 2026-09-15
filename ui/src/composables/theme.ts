import { ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'
const KEY = 'rt-theme'

function stored(): ThemeMode {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

export const themeMode = ref<ThemeMode>(stored())
export const isDark = ref(false)
const media = window.matchMedia('(prefers-color-scheme: dark)')

function apply() {
  isDark.value = themeMode.value === 'dark' || (themeMode.value === 'system' && media.matches)
  document.documentElement.classList.toggle('dark', isDark.value)
}

media.addEventListener('change', apply)
watch(themeMode, (m) => {
  try {
    if (m === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, m)
  } catch {
    /* ignore */
  }
  apply()
})
apply()

export function cycleTheme() {
  themeMode.value = themeMode.value === 'system' ? 'light' : themeMode.value === 'light' ? 'dark' : 'system'
}
