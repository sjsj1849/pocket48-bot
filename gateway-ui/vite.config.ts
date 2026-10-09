import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  base: '/',
  plugins: [react()],
  build: {
    outDir: '../internal/gateway/web',
    emptyOutDir: true,
    target: 'esnext',
  },
  server: {
    port: 5179,
    proxy: { '/api': { target: 'http://127.0.0.1:8790' } },
  },
})
