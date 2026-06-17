import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 产出 web/dist；T8 再 copy 到 internal/webui/dist。base 用相对路径，便于被 Go embed 提供。
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: {
    outDir: 'dist',
  },
  server: {
    proxy: {
      // /v1 与 /health 代理到本地 bridge 服务；SSE 默认 proxy 即可（无需 buffering）
      '/v1': {
        target: 'http://127.0.0.1:8765',
        changeOrigin: true,
      },
      '/health': {
        target: 'http://127.0.0.1:8765',
        changeOrigin: true,
      },
    },
  },
})
