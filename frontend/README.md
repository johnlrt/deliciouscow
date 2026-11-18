# Cordova Dashboard (`frontend/`)

Svelte 5 + SvelteKit 2 (`@sveltejs/adapter-static`) + strict TypeScript dashboard implementing `docs/plan/PLAN-front.md`.

## Scripts
```sh
npm run check   # svelte-kit sync + svelte-check
npm test        # vitest run
npm run build   # builds static SPA to frontend/build/
```
Set `VITE_MOCK=1` to run without a backend server.
