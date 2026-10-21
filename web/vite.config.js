import { defineConfig } from 'vite';

export default defineConfig({
  build: {
    outDir: '../internal/website/dist',
    emptyOutDir: true,
    manifest: true,
    rollupOptions: {
      input: 'resources/js/app.js',
    },
  },
  css: {
    preprocessorOptions: {
      scss: {
        quietDeps: true,
        silenceDeprecations: ['import', 'global-builtin', 'color-functions', 'if-function'],
        loadPaths: ['node_modules'],
      },
    },
  },
});
