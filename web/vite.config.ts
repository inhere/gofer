import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 产出 web/dist；T8 再 copy 到 internal/webui/dist。
// base 用绝对 '/'：SPA 恒挂载在根，资源引用为 /assets/*，这样深层路由(/jobs/:id)
// 直接访问/刷新时浏览器仍请求 /assets/*（而非被解析成 /jobs/:id/assets/* 命中 SPA 兜底导致白屏）。
export default defineConfig({
  plugins: [vue()],
  base: '/',
  build: {
    outDir: 'dist',
  },
  server: {
    proxy: {
      // Q3：浏览器推送 /v1/ws 是 WebSocket，代理要放行 Upgrade（须排在 /v1 之前）；
      // 开发时 Origin 与代理后的 Host 不同，需要在 governance.attach_origins 里放行开发源。
      '/v1/ws': {
        target: 'ws://127.0.0.1:8765',
        ws: true,
        changeOrigin: true,
      },
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
