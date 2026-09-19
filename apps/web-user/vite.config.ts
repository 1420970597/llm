import path from 'path'
import react from '@vitejs/plugin-react'
import semiPkg from '@douyinfe/vite-plugin-semi'
import { defineConfig } from 'vite'

const { vitePluginSemi } = semiPkg as { vitePluginSemi: (options?: { cssLayer?: boolean }) => unknown }

// 构建期版本标识（issue #88）。
//
// 为什么放在这里而不是运行时读：版本号必须跟着**构建产物**走。
// 运行时去查 git 在镜像里查不到（.dockerignore 排除了 .git），
// 去查 API 又只能证明「后端是哪一版」，证明不了「页面上这份 JS 是哪一版」。
// 写进产物后，`curl /version.json` 拿到的就是这份 JS 自己的版本。
const APP_VERSION = (process.env.GIT_SHA ?? '').trim() || 'unknown'
const APP_BUILD_TIME = (process.env.BUILD_TIME ?? '').trim() || new Date().toISOString()

/**
 * 把版本信息作为独立文件写进产物根目录。
 *
 * 为什么不只放进 JS：`curl /version.json` 能直接拿到结构化结果，
 * 不需要下载并解析整包 JS；部署后的版本比对因此可以是一条 5 秒命令
 *（见 scripts/check-deployed-version.sh）。
 *
 * 用 generateBundle 写而不是 buildEnd：这是 Vite 官方推荐的「向产物目录写文件」时机，
 * 能保证文件与 bundle 一同落盘、一同被拷贝进镜像。
 */
function emitVersionJson() {
  return {
    name: 'emit-version-json',
    generateBundle() {
      this.emitFile({
        type: 'asset',
        fileName: 'version.json',
        source: JSON.stringify(
          { version: APP_VERSION, buildTime: APP_BUILD_TIME, source: 'apps/web-user' },
          null,
          2,
        ) + '\n',
      })
    },
  }
}


export default defineConfig({
  define: {
    'import.meta.env.VITE_APP_VERSION': JSON.stringify(APP_VERSION),
    'import.meta.env.VITE_APP_BUILD_TIME': JSON.stringify(APP_BUILD_TIME),
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    host: '0.0.0.0',
    port: 3210,
  },
  plugins: [react(), vitePluginSemi({ cssLayer: true }) as never, emitVersionJson()],
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
