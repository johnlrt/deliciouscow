<script lang="ts">
	import { page } from '$app/stores';
	import { api } from '$lib/api';

	let username = $state('');
	let password = $state('');
	let totpCode = $state('');
	let err = $state('');
	let busy = $state(false);

	async function submit(e: Event) {
		e.preventDefault();
		err = '';
		busy = true;
		try {
			await api.login(username, password, totpCode || undefined);
			const next = $page.url.searchParams.get('next');
			const safeNext = next && next.startsWith('/') && !next.startsWith('//') ? next : '/dashboard';
			location.href = safeNext;
		} catch (x: any) {
			err = x?.message || 'Ошибка входа';
		} finally {
			busy = false;
		}
	}
</script>

<h1>Вход</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}

<form class="col" onsubmit={submit}>
	<label>
		Имя пользователя
		<input bind:value={username} required autocomplete="username" />
	</label>
	<label>
		Пароль
		<input bind:value={password} type="password" required autocomplete="current-password" />
	</label>
	<label>
		Код TOTP (если включён)
		<input bind:value={totpCode} inputmode="numeric" autocomplete="one-time-code" placeholder="6 цифр" />
	</label>
	<button disabled={busy}>{busy ? 'Вход...' : 'Войти'}</button>
</form>

<p class="mut">
	Забыли пароль? <a href="/recover">Восстановить по коду восстановления</a>
</p>
