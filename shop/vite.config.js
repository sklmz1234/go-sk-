import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// proxy 与管理台 frontend/vite.config.js 完全同一套：/api 转发到网关。
const apiProxy = {
  '/api': {
    target: 'http://127.0.0.1:8080',
    changeOrigin: true,
  },
};

export default defineConfig({
  plugins: [react()],
  server: {
    // 5173 被管理台 frontend/ 占用，C 端商城用 5174，两个 dev server 可并行。
    port: 5174,
    proxy: apiProxy,
    // cloudflared 隧道演示：访客经 *.trycloudflare.com 访问本机 dev server，
    // Vite 默认只放行 localhost 域名的 Host 头，必须显式允许，否则全部 403。
    allowedHosts: ['.trycloudflare.com'],
  },
  preview: {
    port: 5174,
    proxy: apiProxy,
  },
});
