import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      // 127.0.0.1 rather than localhost, which Node may resolve to ::1, where
      // a webapp without sign-in doesn't listen.
      '/api': 'http://127.0.0.1:8080',
    },
  },
})
