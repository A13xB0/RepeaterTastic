import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { mockApi } from './mock/index.ts'

// `npm run dev` serves an in-memory mock of docs/api.md.
// `RT_BACKEND=http://raspberrypi:8080 npm run dev` proxies /api to a real daemon instead.
const backend = process.env.RT_BACKEND

export default defineConfig({
  plugins: [vue(), tailwindcss(), ...(backend ? [] : [mockApi()])],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    proxy: backend ? { '/api': { target: backend, changeOrigin: true } } : undefined,
  },
  build: {
    outDir: fileURLToPath(new URL('../internal/web/dist', import.meta.url)),
    emptyOutDir: true,
    target: 'es2022',
    cssCodeSplit: true,
    reportCompressedSize: true,
    chunkSizeWarningLimit: 400,
  },
})
