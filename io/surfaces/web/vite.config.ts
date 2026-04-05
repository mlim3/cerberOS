import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { type ConfigEnv, loadEnv } from 'vite'

export default defineConfig(({ command, mode }) => {
  const env = loadEnv(command, mode, [])
  return {
    plugins: [react()],
    server: {
      proxy: {
        '/api': {
          target: 'http://localhost:3001',
          changeOrigin: true,
        },
      },
    },
    define: {
      'import.meta.env.VITE_DEMO_MODE': JSON.stringify(env.VITE_DEMO_MODE ?? 'false'),
    },
  }
})
