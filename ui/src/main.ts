import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './main.css'
import './composables/theme'

createApp(App).use(router).mount('#app')
