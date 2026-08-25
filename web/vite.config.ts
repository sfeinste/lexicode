/// <reference types="vitest/config" />
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
  test: {
    // globals:true is what lets @testing-library/react register its automatic afterEach
    // cleanup; test files still import describe/it/expect explicitly.
    globals: true,
    environment: 'jsdom',
    // LEXI-13: styles/muiTheme.ts derives the MUI palette from tokens.css via '?raw', so
    // tokens.css stays the single source of colour. Vitest stubs every .css import to an
    // empty string unless CSS processing is on — including '?raw' — which would leave the
    // theme with no palette under test. It also makes CSS-module class names real rather
    // than stubbed, which is what the class-name assertions in the suite already assume.
    css: true,
    include: ['src/**/*.test.{ts,tsx}'],
  },
  server: {
    port: 5173,
    proxy: {
      // `make dev` runs Vite next to the Go server; the SPA talks to the real API through here.
      '/api': 'http://127.0.0.1:7717',
    },
  },
})
