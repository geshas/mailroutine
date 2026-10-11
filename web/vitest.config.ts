import path from 'node:path'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    restoreMocks: true,
    testTimeout: 20_000,
    coverage: {
      provider: 'v8',
      // Application code only: generated routes and the vendored shadcn primitives are excluded.
      include: ['src/lib/**', 'src/components/mr/**', 'src/routes/**'],
      exclude: ['src/routeTree.gen.ts', 'src/**/*.test.{ts,tsx}', 'src/test/**'],
      reporter: ['text', 'lcov'],
      thresholds: { lines: 85, statements: 85, functions: 80, branches: 75 },
    },
  },
})
