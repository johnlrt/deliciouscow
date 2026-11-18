<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import { formatUTC, formatXMR } from '$lib/format';
	import type { Balance, Payout, PayoutSettings } from '$lib/types';

	let balance = $state<Balance | null>(null);
	let settings = $state<PayoutSettings | null>(null);
	let payouts = $state<Payout[]>([]);
	let address = $state('');
	let password = $state('');
	let totpCode = $state('');
	let err = $state('');
	let msg = $state('');

	async function load() {
		try {
			const [b, s, p] = await Promise.all([
				api.getBalance(),
				api.getPayoutSettings(),
				api.listPayouts()
			]);
			balance = b;
			settings = s;
			address = s.address || '';
			payouts = p.data;
		} catch (x: any) {
			err = x?.message || 'Ошибка загрузки выплат';
		}
	}

	onMount(load);

	async function saveAddress(e: Event) {
		e.preventDefault();
		err = '';
		msg = '';
		try {
			const step = await api.stepUp(password, totpCode);
			settings = await api.updatePayoutSettings(address, step.step_up_token);
			password = '';
			totpCode = '';
			msg = 'Адрес выплат сохранён. Действует 24-часовая задержка безопасности.';
		} catch (x: any) {
			err = x?.message || 'Ошибка сохранения адреса';
		}
	}
</script>

<h1>Выплаты владельца нод</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}
{#if msg}<div class="card ok" role="status">{msg}</div>{/if}

<div class="grid">
	<div class="card">
		<div class="mut">Доступно к выплате</div>
		<div class="big">{formatXMR(balance?.earnings_available_piconero)}</div>
	</div>
	<div class="card">
		<div class="mut">Ожидает созревания</div>
		<div class="big">{formatXMR(balance?.earnings_pending_piconero)}</div>
	</div>
	<div class="card">
		<div class="mut">В обработке</div>
		<div class="big">{formatXMR(balance?.payout_reserved_piconero)}</div>
	</div>
</div>

<div class="card">
	<h2>Настройки адреса выплат ({settings?.network || 'stagenet'})</h2>
	<p class="mut">
		Минимальный порог автоматической выплаты: {formatXMR(settings?.minimum_piconero)}.
		{#if settings?.eligible_after}
			Выплаты разблокируются после: {formatUTC(settings.eligible_after)}.
		{/if}
	</p>
	<form class="col" onsubmit={saveAddress}>
		<label>
			Monero-адрес ({settings?.network})
			<input bind:value={address} required placeholder="95 или 106 символов" />
		</label>
		<label>
			Пароль (step-up подтверждение)
			<input bind:value={password} type="password" required />
		</label>
		<label>
			Код TOTP (если включён)
			<input bind:value={totpCode} inputmode="numeric" />
		</label>
		<button>Сохранить адрес</button>
	</form>
</div>

<div class="card">
	<h2>История выплат</h2>
	<table>
		<thead><tr><th>ID</th><th>Сумма</th><th>Статус</th><th>Обновлено</th></tr></thead>
		<tbody>
			{#each payouts as p}
				<tr>
					<td><code>{p.id.slice(0, 12)}</code></td>
					<td>{formatXMR(p.amount_piconero)}</td>
					<td>{p.status}</td>
					<td>{formatUTC(p.updated_at)}</td>
				</tr>
			{:else}
				<tr><td colspan="4" class="mut">Выплат пока нет</td></tr>
			{/each}
		</tbody>
	</table>
</div>
