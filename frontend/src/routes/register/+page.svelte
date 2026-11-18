<script lang="ts">
	import { api } from '$lib/api';

	let username = $state('');
	let password = $state('');
	let ackPrivacy = $state(false);
	let recoveryCodes = $state<string[]>([]);
	let savedAck = $state(false);
	let err = $state('');
	let busy = $state(false);

	async function submit(e: Event) {
		e.preventDefault();
		err = '';
		busy = true;
		try {
			const res = await api.register(username, password, 'v1');
			recoveryCodes = res.recovery_codes;
		} catch (x: any) {
			err = x?.message || 'Ошибка регистрации';
		} finally {
			busy = false;
		}
	}
</script>

<h1>Регистрация</h1>
{#if err}<div class="card err-box" role="alert">{err}</div>{/if}

{#if recoveryCodes.length > 0}
	<div class="card warn" role="region" aria-label="Коды восстановления">
		<h2>Сохраните коды восстановления</h2>
		<p>Эти 8 одноразовых кодов показываются только один раз. Они понадобятся, если вы потеряете пароль или TOTP.</p>
		<pre>{recoveryCodes.join('\n')}</pre>
		<label class="chk">
			<input type="checkbox" bind:checked={savedAck} />
			Я сохранил коды восстановления в надёжном месте
		</label>
		<p>
			<button disabled={!savedAck} onclick={() => (location.href = '/dashboard')}>Перейти в кабинет</button>
		</p>
	</div>
{:else}
	<form class="col" onsubmit={submit}>
		<label>
			Имя пользователя (3–64 символа)
			<input bind:value={username} minlength="3" maxlength="64" required autocomplete="username" />
		</label>
		<label>
			Пароль (12–128 символов)
			<input bind:value={password} type="password" minlength="12" maxlength="128" required autocomplete="new-password" />
		</label>
		<label class="chk">
			<input type="checkbox" bind:checked={ackPrivacy} required />
			Я прочитал(а) <a href="/docs/privacy">уведомление о приватности</a> и понимаю, что ноды видят запросы открытым текстом
		</label>
		<button disabled={busy || !ackPrivacy}>{busy ? 'Создание...' : 'Создать аккаунт'}</button>
	</form>
{/if}
