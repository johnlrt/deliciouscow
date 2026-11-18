<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { formatUTC, formatXMR } from '$lib/format';
	import type { ApiKey } from '$lib/types';

	let keys = $state<ApiKey[]>([]);
	let name = $state('');
	let capXMR = $state('');
	let revealedSecret = $state('');
	let err = $state('');

	async function load() {
		try {
			keys = (await api.listKeys()).data;
		} catch (x: any) {
			err = x?.message || 'Ошибка загрузки ключей';
		}
	}

	onMount(load);

	async function create(e: Event) {
		e.preventDefault();
		err = '';
		revealedSecret = '';
		let capPico: string | null = null;
		if (capXMR.trim()) {
			const num = Number(capXMR);
			if (!Number.isFinite(num) || num <= 0) {
				err = 'Некорректный дневной лимит XMR';
				return;
			}
			capPico = Math.floor(num * 1e12).toString();
		}
		try {
			const res = await api.createKey({
				name,
				permissions: ['chat:write', 'usage:read'],
				model_ids: [],
				daily_spend_cap_piconero: capPico
			});
			revealedSecret = res.secret;
			name = '';
			capXMR = '';
			await load();
		} catch (x: any) {
			err = x?.message || 'Не удалось создать ключ';
		}
	}

	async function revoke(id: string) {
		if (!confirm('Удалить API-ключ? Активные запросы по нему будут остановлены.')) return;
		await api.deleteKey(id);
		await load();
	}
</script>

<h1>API-ключи</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}

<div class="card">
	<h2>Создать ключ</h2>
	<form class="row" onsubmit={create}>
		<input bind:value={name} placeholder="Название ключа" required aria-label="Название ключа" />
		<input bind:value={capXMR} placeholder="Дневной лимит XMR (опц.)" aria-label="Дневной лимит XMR" />
		<button>Создать</button>
	</form>
	{#if revealedSecret}
		<div class="card warn" style="margin-top: 12px;" role="status">
			<strong>Скопируйте секрет сейчас — он больше не будет показан:</strong>
			<pre>{revealedSecret}</pre>
		</div>
	{/if}
</div>

<div class="card">
	<table>
		<thead>
			<tr><th>Название</th><th>Префикс</th><th>Права</th><th>Дневной лимит</th><th>Использован</th><th></th></tr>
		</thead>
		<tbody>
			{#each keys as k}
				<tr>
					<td>{k.name}</td>
					<td><code>{k.prefix}…</code></td>
					<td>{k.permissions.join(', ')}</td>
					<td>{k.daily_spend_cap_piconero ? formatXMR(k.daily_spend_cap_piconero) : 'Без лимита'}</td>
					<td>{formatUTC(k.last_used_at)}</td>
					<td><button class="del" onclick={() => revoke(k.id)}>Отозвать</button></td>
				</tr>
			{:else}
				<tr><td colspan="6" class="mut">Ключей пока нет</td></tr>
			{/each}
		</tbody>
	</table>
</div>
