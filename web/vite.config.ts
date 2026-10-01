import { svelte } from '@sveltejs/vite-plugin-svelte'
import { defineConfig } from 'vite'

// During `npm run dev`, API/media/WebSocket calls go to the Go server on :8080.
export default defineConfig({
  plugins: [svelte()],
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
      '/media': 'http://localhost:8080',
    },
  },
})
