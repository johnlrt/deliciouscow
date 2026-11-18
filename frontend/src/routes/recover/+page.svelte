<script lang="ts">
	import { api } from '$lib/api';

	let username = $state('');
	let recoveryCode = $state('');
	let newPassword = $state('');
	let err = $state('');
	let busy = $state(false);

	async function submit(e: Event) {
		e.preventDefault();
		err = '';
		busy = true;
		try {
			await api.recover(username, recoveryCode, newPassword);
			location.href = '/dashboard';
		} catch (x: any) {
			err = x?.message || 'Ошибка восстановления';
		} finally {
			busy = false;
		}
	}
</script>

<h1>Восстановление аккаунта</h1>
<div class="card warn">
	После восстановления пароля все текущие сессии и TOTP будут сброшены, а выплаты заблокированы на 24 часа.
</div>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}

<form class="col" onsubmit={submit}>
	<label>
		Имя пользователя
		<input bind:value={username} required />
	</label>
	<label>
		Одноразовый код восстановления
		<input bind:value={recoveryCode} required placeholder="xxxx-xxxx-xxxx-xxxx" />
	</label>
	<label>
		Новый пароль (12–128 символов)
		<input bind:value={newPassword} type="password" minlength="12" maxlength="128" required />
	</label>
	<button disabled={busy}>{busy ? 'Сброс...' : 'Восстановить доступ'}</button>
</form>
