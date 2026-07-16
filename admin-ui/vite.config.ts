import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  base: '/pocket48/',
  plugins: [react()],
  build: {
    outDir: '../internal/admin/web',
    emptyOutDir: true,
    target: 'esnext',
  },
  server: {
    port: 5178,
    proxy: {
      '/pocket48/api': {
        target: 'http://127.0.0.1:8787',
        rewrite: (path) => path.replace(/^\/pocket48/, ''),
        ws: true,
      },
    },
  },
})
