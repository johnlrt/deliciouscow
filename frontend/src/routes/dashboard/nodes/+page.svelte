<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api';
	import { connectLive } from '$lib/live';
	import { formatUTC } from '$lib/format';
	import type { Node } from '$lib/types';

	let nodes = $state<Node[]>([]);
	let search = $state('');
	let statusFilter = $state('all');
	let userCode = $state('');
	let nodeName = $state('');
	let err = $state('');
	let msg = $state('');

	async function load() {
		try {
			nodes = (await api.listNodes()).data;
		} catch (x: any) {
			err = x?.message || 'Ошибка загрузки нод';
		}
	}

	onMount(() => {
		const codeParam = $page.url.searchParams.get('code');
		if (codeParam) userCode = codeParam;
		load();
		return connectLive((topic) => {
			if (topic === 'nodes') load();
		});
	});

	async function link(e: Event) {
		e.preventDefault();
		err = '';
		msg = '';
		try {
			const n = await api.linkNode(userCode, nodeName);
			msg = `Нода «${n.name}» привязана.`;
			userCode = '';
			nodeName = '';
			await load();
		} catch (x: any) {
			err = x?.message || 'Не удалось привязать ноду';
		}
	}

	let filtered = $derived(
		nodes.filter((n) => {
			if (statusFilter !== 'all' && n.status !== statusFilter) return false;
			if (search && !n.name.toLowerCase().includes(search.toLowerCase()) && !n.id.includes(search)) {
				return false;
			}
			return true;
		})
	);
</script>

<h1>Ноды</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}
{#if msg}<div class="card ok" role="status">{msg}</div>{/if}

<div class="card">
	<h2>Привязать новую ноду</h2>
	<p class="mut">Запустите <code>cordova-node login --server {location.origin}</code> на машине с нодой и введите показанный код.</p>
	<form class="row" onsubmit={link}>
		<input bind:value={userCode} placeholder="Код (XXXX-XXXX)" required aria-label="Код привязки" />
		<input bind:value={nodeName} placeholder="Название ноды" aria-label="Название ноды" />
		<button>Привязать</button>
	</form>
</div>

<div class="card">
	<div class="row" style="margin-bottom: 12px; display: flex; gap: 8px;">
		<input bind:value={search} placeholder="Поиск по имени или ID..." aria-label="Поиск нод" />
		<select bind:value={statusFilter} aria-label="Фильтр по статусу">
			<option value="all">Все статусы</option>
			<option value="online">Онлайн</option>
			<option value="offline">Офлайн</option>
			<option value="revoked">Отозвана</option>
		</select>
	</div>
	<table>
		<thead>
			<tr><th>Название</th><th>Статус</th><th>Слоты</th><th>Последний контакт</th></tr>
		</thead>
		<tbody>
			{#each filtered as n}
				<tr>
					<td><a href="/dashboard/nodes/{n.id}">{n.name}</a></td>
					<td class={n.status === 'online' ? 'ok' : n.status === 'revoked' ? 'bad' : 'mut'}>{n.status}</td>
					<td>{n.active_slots} / {n.max_slots}</td>
					<td>{formatUTC(n.last_seen_at)}</td>
				</tr>
			{:else}
				<tr><td colspan="4" class="mut">Ноды не найдены</td></tr>
			{/each}
		</tbody>
	</table>
</div>
