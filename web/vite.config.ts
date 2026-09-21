import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// https://vite.dev/config/
export default defineConfig({
    plugins: [react()],
    test: {
        environment: 'jsdom',
    },
    server: {
        port: 3000,
        host: true,
        // 开发模式代理到本地后端；生产模式后端直接内嵌并服务 dist。
        proxy: {
            '/api': {
                target: 'http://127.0.0.1:9466',
                changeOrigin: true,
            },
            // /shared 直链与浏览页深链接也由后端服务（显式注册的路由）。
            '/shared': {
                target: 'http://127.0.0.1:9466',
                changeOrigin: true,
            },
        },
    },
    build: {
        outDir: 'dist',
        emptyOutDir: true,
        sourcemap: false,
    },
})
