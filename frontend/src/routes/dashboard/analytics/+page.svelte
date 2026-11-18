<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { formatUTC, formatXMR } from '$lib/format';
	import type { Analytics, UsageRow } from '$lib/types';

	let analytics = $state<Analytics | null>(null);
	let usage = $state<UsageRow[]>([]);
	let modelFilter = $state('');
	let bucket = $state<'day' | 'hour'>('day');
	let err = $state('');

	async function load() {
		try {
			const [a, u] = await Promise.all([api.getAnalytics(bucket), api.listUsage(modelFilter)]);
			analytics = a;
			usage = u.data;
		} catch (x: any) {
			err = x?.message || 'Ошибка загрузки аналитики';
		}
	}

	onMount(load);

	let maxReqs = $derived(Math.max(1, ...(analytics?.buckets.map((b) => b.requests) ?? [1])));
</script>

<h1>Аналитика</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}

<div class="card row" style="display:flex; gap:10px; align-items:center;">
	<label>
		Гранулярность
		<select bind:value={bucket} onchange={load}>
			<option value="day">По дням (UTC)</option>
			<option value="hour">По часам (UTC)</option>
		</select>
	</label>
	<label>
		Фильтр по модели
		<input bind:value={modelFilter} placeholder="например cordova-tiny-chat" onchange={load} />
	</label>
</div>

{#if analytics}
	<div class="card">
		<h2>График запросов (UTC)</h2>
		<div class="bars" aria-hidden="true">
			{#each analytics.buckets as b}
				<div style="height: {(b.requests / maxReqs) * 100}%" title="{b.start}: {b.requests}"></div>
			{/each}
		</div>
		<table>
			<thead>
				<tr><th>Период</th><th>Запросы</th><th>Вход</th><th>Выход</th><th>Расход</th><th>p50 мс</th><th>p95 мс</th></tr>
			</thead>
			<tbody>
				{#each analytics.buckets as b}
					<tr>
						<td>{formatUTC(b.start)}</td>
						<td>{b.requests}</td>
						<td>{b.input_tokens}</td>
						<td>{b.output_tokens}</td>
						<td>{formatXMR(b.spend_piconero)}</td>
						<td>{b.p50_latency_ms ?? '—'}</td>
						<td>{b.p95_latency_ms ?? '—'}</td>
					</tr>
				{:else}
					<tr><td colspan="7" class="mut">Данных за период нет</td></tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<div class="card">
	<h2>Последние запросы</h2>
	<table>
		<thead><tr><th>Время</th><th>Модель</th><th>Статус</th><th>Токены (вх/вых)</th><th>Списано</th><th>Задержка</th></tr></thead>
		<tbody>
			{#each usage as u}
				<tr>
					<td>{formatUTC(u.created_at)}</td>
					<td><code>{u.model_id}</code></td>
					<td class={u.status === 'settled' ? 'ok' : 'mut'}>{u.status}</td>
					<td>{u.input_tokens} / {u.output_tokens}</td>
					<td>{u.charged_piconero ? formatXMR(u.charged_piconero) : '—'}</td>
					<td>{u.latency_ms !== null ? `${u.latency_ms} мс` : '—'}</td>
				</tr>
			{:else}
				<tr><td colspan="6" class="mut">Запросов пока нет</td></tr>
			{/each}
		</tbody>
	</table>
</div>
