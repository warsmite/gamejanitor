import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  server: {
    proxy: {
      // SSE endpoint needs special handling — disable buffering
      '/api/events': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        headers: { 'Cache-Control': 'no-transform' },
        configure: (proxy) => {
          proxy.on('proxyRes', (proxyRes) => {
            // Prevent Vite from buffering the SSE stream
            proxyRes.headers['cache-control'] = 'no-cache';
            proxyRes.headers['x-accel-buffering'] = 'no';
          });
        },
      },
      // Regular API calls
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      // Game assets (icons)
      '/games': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
});
