<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api';
	import type { SessionView } from '$lib/types';

	let session = $state<SessionView | null>(null);
	let password = $state('');
	let currentTotp = $state('');
	let setupId = $state('');
	let otpauthUri = $state('');
	let confirmCode = $state('');
	let err = $state('');
	let msg = $state('');

	async function load() {
		session = await api.getSession();
	}

	onMount(load);

	async function startTOTP(e: Event) {
		e.preventDefault();
		err = '';
		try {
			const res = await api.setupTOTP(password, session?.totp_enabled ? currentTotp : undefined);
			setupId = res.setup_id;
			otpauthUri = res.otpauth_uri;
			password = '';
			currentTotp = '';
		} catch (x: any) {
			err = x?.message || 'Ошибка настройки TOTP';
		}
	}

	async function confirmTOTP(e: Event) {
		e.preventDefault();
		err = '';
		try {
			await api.confirmTOTP(setupId, confirmCode);
			setupId = '';
			otpauthUri = '';
			confirmCode = '';
			msg = 'TOTP успешно активирован.';
			await load();
		} catch (x: any) {
			err = x?.message || 'Неверный код TOTP';
		}
	}
</script>

<h1>Безопасность</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}
{#if msg}<div class="card ok" role="status">{msg}</div>{/if}

{#if session}
	<div class="card">
		<p>Аккаунт: <strong>{session.username}</strong></p>
		<p>TOTP (2FA): <strong>{session.totp_enabled ? 'Включён' : 'Отключён'}</strong></p>
		<p>Осталось кодов восстановления: <strong>{session.recovery_codes_remaining}</strong></p>
	</div>
{/if}

<div class="card">
	<h2>Настройка TOTP</h2>
	{#if !setupId}
		<form class="col" onsubmit={startTOTP}>
			<label>
				Текущий пароль
				<input bind:value={password} type="password" required />
			</label>
			{#if session?.totp_enabled}
				<label>
					Текущий код TOTP
					<input bind:value={currentTotp} required inputmode="numeric" />
				</label>
			{/if}
			<button>Начать настройку TOTP</button>
		</form>
	{:else}
		<p>Добавьте этот URI в приложение-аутентификатор:</p>
		<pre>{otpauthUri}</pre>
		<form class="row" onsubmit={confirmTOTP}>
			<input bind:value={confirmCode} placeholder="6-значный код" required inputmode="numeric" />
			<button>Подтвердить</button>
		</form>
	{/if}
</div>
