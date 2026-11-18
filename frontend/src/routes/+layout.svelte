<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api';
	import type { SessionView } from '$lib/types';

	let { children } = $props();
	let session = $state<SessionView | null>(null);
	let loaded = $state(false);

	const navLinks = [
		['/dashboard', 'Обзор'],
		['/dashboard/playground', 'Playground'],
		['/dashboard/nodes', 'Ноды'],
		['/dashboard/keys', 'Ключи'],
		['/dashboard/billing', 'Баланс'],
		['/dashboard/billing/payouts', 'Выплаты'],
		['/dashboard/analytics', 'Аналитика'],
		['/dashboard/settings/security', 'Безопасность'],
		['/docs', 'Документация']
	];

	async function refreshSession() {
		try {
			session = await api.getSession();
		} catch {
			session = null;
		} finally {
			loaded = true;
		}
	}

	onMount(() => {
		refreshSession();
	});

	$effect(() => {
		const path = $page.url.pathname;
		if (loaded && path.startsWith('/dashboard') && !session) {
			goto(`/login?next=${encodeURIComponent(path)}`);
		}
	});

	async function doLogout() {
		await api.logout().catch(() => {});
		session = null;
		goto('/');
	}
</script>

<header>
	<a href="/" class="logo">◈ Cordova</a>
	<nav aria-label="Основная навигация">
		{#if session}
			{#each navLinks as [href, label]}
				<a {href} class={$page.url.pathname === href ? 'on' : ''}>{label}</a>
			{/each}
			<button class="sec" onclick={doLogout}>Выйти ({session.username})</button>
		{:else}
			<a href="/docs" class={$page.url.pathname.startsWith('/docs') ? 'on' : ''}>Документация</a>
			<a href="/login" class={$page.url.pathname === '/login' ? 'on' : ''}>Вход</a>
			<a href="/register" class={$page.url.pathname === '/register' ? 'on' : ''}>Регистрация</a>
		{/if}
	</nav>
</header>

<main>
	{@render children()}
</main>
