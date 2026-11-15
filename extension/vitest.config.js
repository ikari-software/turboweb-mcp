import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    setupFiles: ['./setup.js'],
    // Vitest 5 defaults clearMocks to true, wiping call history before
    // every test — including calls made in beforeAll module loading
    // (popup.js connecting its port). Tests here reset their own mocks.
    clearMocks: false,
    include: ['__tests__/**/*.test.js'],
  },
});
