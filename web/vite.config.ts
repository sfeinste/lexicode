import { writeFileSync } from 'node:fs'
import { join } from 'node:path'

import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

const OUT_DIR = 'dist'

/**
 * The Go binary embeds `web/dist` with `//go:embed all:dist`, and go:embed refuses to compile when
 * a pattern matches nothing. `dist/.gitkeep` is committed so that a clean checkout builds; Vite's
 * `emptyOutDir` would delete it on every build and leave the working tree dirty. This plugin puts
 * it back after the bundle is written.
 */
function keepDistTracked(): Plugin {
  return {
    name: 'lexicode-keep-dist-tracked',
    apply: 'build',
    closeBundle() {
      writeFileSync(join(OUT_DIR, '.gitkeep'), '')
    },
  }
}

export default defineConfig({
  plugins: [react(), keepDistTracked()],
  build: {
    outDir: OUT_DIR,
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      // `make dev` runs Vite next to the Go server; the SPA talks to the real API through here.
      '/api': 'http://127.0.0.1:7717',
    },
  },
})
