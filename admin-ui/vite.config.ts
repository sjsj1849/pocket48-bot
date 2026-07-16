import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  base: '/',
  plugins: [react()],
  build: {
    outDir: '../internal/admin/web',
    emptyOutDir: true,
    target: 'esnext',
  },
  server: {
    port: 5178,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8787',
        ws: true,
      },
    },
  },
})
