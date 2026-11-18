<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api';
	import { formatBytes, formatUTC } from '$lib/format';
	import type { Node } from '$lib/types';

	let node = $state<Node | null>(null);
	let newName = $state('');
	let err = $state('');
	let msg = $state('');

	async function load() {
		try {
			node = await api.getNode($page.params.id ?? '');
			newName = node.name;
		} catch (x: any) {
			err = x?.message || 'Нода не найдена';
		}
	}

	onMount(load);

	async function rename(e: Event) {
		e.preventDefault();
		if (!node) return;
		err = '';
		try {
			node = await api.renameNode(node.id, newName);
			msg = 'Название обновлено.';
		} catch (x: any) {
			err = x?.message || 'Ошибка переименования';
		}
	}

	async function revoke() {
		if (!node || !confirm('Отозвать ноду? Все активные задания будут прерваны.')) return;
		await api.revokeNode(node.id);
		await load();
	}
</script>

<p><a href="/dashboard/nodes">← К списку нод</a></p>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}
{#if msg}<div class="card ok" role="status">{msg}</div>{/if}

{#if node}
	<h1>{node.name}</h1>
	<div class="grid">
		<div class="card">
			<div class="mut">Статус</div>
			<div class="big">{node.status}</div>
			<div class="mut">Версия: {node.node_version || '—'}</div>
		</div>
		<div class="card">
			<div class="mut">Железо</div>
			<div>CPU: {node.hardware?.cpu_model || '—'} ({node.hardware?.cpu_cores ?? 0} ядер)</div>
			<div>RAM: {formatBytes(node.hardware?.ram_bytes)}</div>
			{#if node.hardware?.gpu_model}
				<div>GPU: {node.hardware.gpu_model} ({formatBytes(node.hardware.vram_bytes)})</div>
			{/if}
		</div>
		<div class="card">
			<div class="mut">Ранг и пропускная способность</div>
			<div>Rank: {node.rank_score.toFixed(2)}</div>
			<div>Throughput: {node.throughput_tps ? `${node.throughput_tps.toFixed(1)} tok/s` : '—'}</div>
			<div class="mut">Создана: {formatUTC(node.created_at)}</div>
		</div>
	</div>

	<div class="card">
		<h2>Модели на ноде</h2>
		<table>
			<thead><tr><th>Модель</th><th>Состояние</th><th>Слоты</th></tr></thead>
			<tbody>
				{#each node.models as m}
					<tr><td><code>{m.model_id}</code></td><td>{m.state}</td><td>{m.active_slots} / {m.max_slots}</td></tr>
				{:else}
					<tr><td colspan="3" class="mut">Нет загруженных моделей</td></tr>
				{/each}
			</tbody>
		</table>
	</div>

	<div class="card">
		<h2>Управление</h2>
		<form class="row" onsubmit={rename}>
			<input bind:value={newName} required aria-label="Новое имя ноды" />
			<button>Переименовать</button>
			{#if node.status !== 'revoked'}
				<button type="button" class="del" onclick={revoke}>Отозвать ноду</button>
			{/if}
		</form>
	</div>
{/if}
