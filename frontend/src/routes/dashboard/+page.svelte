<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { connectLive } from '$lib/live';
	import { formatXMR } from '$lib/format';
	import type { Balance, Node, UsageRow } from '$lib/types';

	let balance = $state<Balance | null>(null);
	let nodes = $state<Node[]>([]);
	let usage = $state<UsageRow[]>([]);
	let err = $state('');

	async function load() {
		try {
			const [b, n, u] = await Promise.all([api.getBalance(), api.listNodes(), api.listUsage()]);
			balance = b;
			nodes = n.data;
			usage = u.data;
		} catch (x: any) {
			err = x?.message || 'Ошибка загрузки';
		}
	}

	onMount(() => {
		load();
		return connectLive(() => load());
	});

	let onlineCount = $derived(nodes.filter((n) => n.status === 'online').length);
</script>

<h1>Обзор</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}

<div class="grid">
	<div class="card">
		<div class="mut">Доступный баланс</div>
		<div class="big">{formatXMR(balance?.available_piconero)}</div>
		<div class="mut">Резерв: {formatXMR(balance?.reserved_piconero)}</div>
		<a href="/dashboard/billing">Пополнить</a>
	</div>
	<div class="card">
		<div class="mut">Заработок владельца</div>
		<div class="big">{formatXMR(balance?.earnings_available_piconero)}</div>
		<div class="mut">Ожидает созревания: {formatXMR(balance?.earnings_pending_piconero)}</div>
		<a href="/dashboard/billing/payouts">Выплаты</a>
	</div>
	<div class="card">
		<div class="mut">Мои ноды</div>
		<div class="big">{onlineCount} / {nodes.length} онлайн</div>
		<a href="/dashboard/nodes">Управление нодами</a>
	</div>
	<div class="card">
		<div class="mut">Запросы</div>
		<div class="big">{usage.length}</div>
		<a href="/dashboard/analytics">Аналитика</a>
	</div>
</div>

{#if balance && balance.available_piconero === '0' && nodes.length === 0}
	<div class="card">
		<strong>Следующий шаг:</strong> <a href="/dashboard/billing">пополните баланс</a> и
		<a href="/dashboard/keys">создайте API-ключ</a>, или <a href="/dashboard/nodes">привяжите свою ноду</a>.
	</div>
{/if}
