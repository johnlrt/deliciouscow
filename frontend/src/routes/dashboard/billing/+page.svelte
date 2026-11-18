<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { connectLive } from '$lib/live';
	import { formatUTC, formatXMR } from '$lib/format';
	import type { Balance, Deposit, DepositAddress, LedgerView } from '$lib/types';

	let balance = $state<Balance | null>(null);
	let addr = $state<DepositAddress | null>(null);
	let deposits = $state<Deposit[]>([]);
	let ledger = $state<LedgerView[]>([]);
	let devAmount = $state('100000000000');
	let err = $state('');
	let msg = $state('');

	async function load() {
		try {
			const [b, a, d, h] = await Promise.all([
				api.getBalance(),
				api.getDepositAddress(),
				api.listDeposits(),
				api.listLedgerHistory()
			]);
			balance = b;
			addr = a;
			deposits = d.data;
			ledger = h.data;
		} catch (x: any) {
			err = x?.message || 'Ошибка загрузки биллинга';
		}
	}

	onMount(() => {
		load();
		return connectLive((topic) => {
			if (topic === 'billing') load();
		});
	});

	async function simulateDeposit(e: Event) {
		e.preventDefault();
		err = '';
		msg = '';
		try {
			await api.devDeposit(devAmount);
			msg = 'Тестовый депозит зачислен.';
			await load();
		} catch (x: any) {
			err = x?.message || 'Ошибка тестового депозита';
		}
	}
</script>

<h1>Баланс и депозиты</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}
{#if msg}<div class="card ok" role="status">{msg}</div>{/if}

<div class="grid">
	<div class="card">
		<div class="mut">Доступно</div>
		<div class="big">{formatXMR(balance?.available_piconero)}</div>
	</div>
	<div class="card">
		<div class="mut">В резерве</div>
		<div class="big">{formatXMR(balance?.reserved_piconero)}</div>
	</div>
	<div class="card">
		<div class="mut">Заработок (доступен / ожидает)</div>
		<div class="big">{formatXMR(balance?.earnings_available_piconero)}</div>
		<div class="mut">Pending: {formatXMR(balance?.earnings_pending_piconero)}</div>
	</div>
</div>

{#if addr}
	<div class="card">
		<h2>Адрес пополнения ({addr.network})</h2>
		<p>Требуется подтверждений: <strong>{addr.confirmations_required}</strong></p>
		<pre>{addr.address}</pre>
		<p class="mut">URI: <code>{addr.uri}</code></p>
	</div>
{/if}

<div class="card">
	<h2>Тестовое пополнение (DEV_MODE)</h2>
	<form class="row" onsubmit={simulateDeposit}>
		<input bind:value={devAmount} aria-label="Сумма в piconero" />
		<button class="sec">Зачислить piconero</button>
	</form>
</div>

<div class="card">
	<h2>Депозиты</h2>
	<table>
		<thead><tr><th>TxID</th><th>Сумма</th><th>Подтверждения</th><th>Статус</th><th>Время</th></tr></thead>
		<tbody>
			{#each deposits as d}
				<tr>
					<td><code>{d.txid.slice(0, 14)}…</code></td>
					<td>{formatXMR(d.amount_piconero)}</td>
					<td>{d.confirmations}</td>
					<td>{d.status}</td>
					<td>{formatUTC(d.observed_at)}</td>
				</tr>
			{:else}
				<tr><td colspan="5" class="mut">Депозитов пока нет</td></tr>
			{/each}
		</tbody>
	</table>
</div>

<div class="card">
	<h2>История операций (Ledger)</h2>
	<table>
		<thead><tr><th>Тип</th><th>Сумма</th><th>Баланс после</th><th>Время</th></tr></thead>
		<tbody>
			{#each ledger as l}
				<tr>
					<td>{l.kind}</td>
					<td>{formatXMR(l.amount_piconero)}</td>
					<td>{formatXMR(l.balance_after_piconero)}</td>
					<td>{formatUTC(l.created_at)}</td>
				</tr>
			{:else}
				<tr><td colspan="4" class="mut">Операций пока нет</td></tr>
			{/each}
		</tbody>
	</table>
</div>
