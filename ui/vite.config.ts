import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [svelte()],
  resolve: {
    alias: {
      '$lib': '/src/lib',
    },
  },
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
      '/assets/games': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
  },
});
