import path from 'path'
import react from '@vitejs/plugin-react'
import semiPkg from '@douyinfe/vite-plugin-semi'
import { defineConfig } from 'vite'

const { vitePluginSemi } = semiPkg as { vitePluginSemi: (options?: { cssLayer?: boolean }) => unknown }

export default defineConfig({
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  plugins: [react(), vitePluginSemi({ cssLayer: true }) as never],
  server: {
    host: '0.0.0.0',
    port: 3210,
  },
  build: {
    rollupOptions: {
      output: {
        manualChunks: {
          'react-core': ['react', 'react-dom', 'react-router-dom'],
          'semi-ui': ['@douyinfe/semi-icons', '@douyinfe/semi-ui'],
          'console-tools': ['axios', 'clsx', 'i18next', 'react-i18next'],
        },
      },
    },
  },
})
