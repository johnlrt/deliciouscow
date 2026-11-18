import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		proxy: {
			'/api': { target: 'http://127.0.0.1:8443', ws: true },
			'/v1': { target: 'http://127.0.0.1:8443' }
		}
	}
});
