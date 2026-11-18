<script lang="ts">
	import { onMount } from 'svelte';
	import { api, ApiError } from '$lib/api';
	import type { Model } from '$lib/types';

	interface ChatMessage {
		role: 'system' | 'user' | 'assistant';
		content: string;
	}

	let models = $state<Model[]>([]);
	let selectedModel = $state('');
	let apiKey = $state('');
	let systemPrompt = $state('Ты полезный AI-ассистент.');
	let promptInput = $state('');
	let streamMode = $state(true);
	let maxTokens = $state(256);
	let temperature = $state(0.7);

	let messages = $state<ChatMessage[]>([]);
	let loading = $state(false);
	let creatingKey = $state(false);
	let err = $state('');
	let lastUsage = $state<{
		prompt_tokens: number;
		completion_tokens: number;
		total_tokens: number;
		latency_ms: number;
	} | null>(null);

	let abortCtrl: AbortController | null = null;

	onMount(async () => {
		try {
			const res = await api.listModels();
			models = res.data || [];
			const avail = models.find((m) => m.availability === 'available') || models[0];
			if (avail) selectedModel = avail.id;
		} catch (e) {
			err = e instanceof ApiError ? e.message : 'Не удалось загрузить список моделей';
		}
	});

	async function createPlaygroundKey() {
		creatingKey = true;
		err = '';
		try {
			const res = await api.createKey({
				name: `playground-${new Date().toISOString().slice(11, 19)}`,
				permissions: ['chat:write'],
				model_ids: [],
				daily_spend_cap_piconero: null
			});
			apiKey = res.secret;
		} catch (e) {
			err = e instanceof ApiError ? e.message : 'Не удалось создать API-ключ';
		} finally {
			creatingKey = false;
		}
	}

	function clearChat() {
		messages = [];
		lastUsage = null;
		err = '';
	}

	function stopGeneration() {
		abortCtrl?.abort();
	}

	async function sendMessage(e: Event) {
		e.preventDefault();
		if (!promptInput.trim() || loading) return;
		if (!apiKey.trim()) {
			err = 'Введите API-ключ (cvk_...) или создайте его кнопкой выше.';
			return;
		}
		if (!selectedModel) {
			err = 'Выберите модель.';
			return;
		}

		err = '';
		lastUsage = null;
		const userText = promptInput.trim();
		promptInput = '';

		messages = [...messages, { role: 'user', content: userText }];
		const payloadMessages: ChatMessage[] = [
			...(systemPrompt.trim() ? [{ role: 'system' as const, content: systemPrompt.trim() }] : []),
			...messages
		];

		messages = [...messages, { role: 'assistant', content: '' }];
		const assistantIdx = messages.length - 1;

		loading = true;
		abortCtrl = new AbortController();
		const startedAt = performance.now();

		try {
			const res = await fetch('/v1/chat/completions', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					Authorization: `Bearer ${apiKey.trim()}`
				},
				body: JSON.stringify({
					model: selectedModel,
					messages: payloadMessages,
					max_tokens: Number(maxTokens) || 256,
					temperature: Number(temperature),
					stream: streamMode,
					...(streamMode ? { stream_options: { include_usage: true } } : {})
				}),
				signal: abortCtrl.signal
			});

			if (!res.ok) {
				const data = await res.json().catch(() => ({}));
				throw new Error(data?.error?.message || `HTTP ${res.status}`);
			}

			if (!streamMode) {
				const data = await res.json();
				const content = data?.choices?.[0]?.message?.content ?? '';
				messages[assistantIdx] = { role: 'assistant', content };
				if (data?.usage) {
					lastUsage = {
						...data.usage,
						latency_ms: Math.round(performance.now() - startedAt)
					};
				}
				return;
			}

			const reader = res.body?.getReader();
			if (!reader) throw new Error('Поток ответа недоступен');
			const decoder = new TextDecoder();
			let buffer = '';

			while (true) {
				const { done, value } = await reader.read();
				if (done) break;
				buffer += decoder.decode(value, { stream: true });

				let nlIdx: number;
				while ((nlIdx = buffer.indexOf('\n')) !== -1) {
					const line = buffer.slice(0, nlIdx).trim();
					buffer = buffer.slice(nlIdx + 1);
					if (!line || line.startsWith(':') || !line.startsWith('data:')) continue;
					const raw = line.slice(5).trim();
					if (raw === '[DONE]') continue;

					let parsed: any;
					try {
						parsed = JSON.parse(raw);
					} catch {
						continue;
					}
					if (parsed?.error) {
						throw new Error(parsed.error.message || parsed.error.code || 'Ошибка потока');
					}
					const delta = parsed?.choices?.[0]?.delta?.content;
					if (typeof delta === 'string' && delta.length > 0) {
						messages[assistantIdx] = {
							role: 'assistant',
							content: messages[assistantIdx].content + delta
						};
					}
					if (parsed?.usage) {
						lastUsage = {
							...parsed.usage,
							latency_ms: Math.round(performance.now() - startedAt)
						};
					}
				}
			}
		} catch (e: any) {
			if (e?.name === 'AbortError') {
				err = 'Генерация остановлена пользователем.';
			} else {
				err = e?.message || 'Ошибка выполнения запроса';
				if (!messages[assistantIdx].content) {
					messages = messages.slice(0, assistantIdx);
				}
			}
		} finally {
			loading = false;
			abortCtrl = null;
		}
	}
</script>

<h1>Интерактивный Playground</h1>

{#if err}
	<div class="card err-box" role="alert">{err}</div>
{/if}

<div class="card">
	<div class="grid">
		<label>
			Модель
			<select bind:value={selectedModel}>
				{#each models as m}
					<option value={m.id}>
						{m.name} ({m.availability === 'available' ? 'онлайн' : m.availability})
					</option>
				{/each}
			</select>
		</label>

		<label>
			API-ключ (`cvk_...`)
			<input type="password" bind:value={apiKey} placeholder="cvk_..." autocomplete="off" />
		</label>

		<label>
			Max tokens
			<input type="number" min="1" max="4096" bind:value={maxTokens} />
		</label>

		<label>
			Temperature
			<input type="number" min="0" max="2" step="0.1" bind:value={temperature} />
		</label>
	</div>

	<div style="display: flex; gap: 12px; align-items: center; margin-top: 12px; flex-wrap: wrap;">
		<button type="button" class="sec" onclick={createPlaygroundKey} disabled={creatingKey}>
			{creatingKey ? 'Создание...' : 'Создать ключ для Playground'}
		</button>
		<label class="chk">
			<input type="checkbox" bind:checked={streamMode} />
			Потоковый вывод (SSE)
		</label>
		<button type="button" class="sec" onclick={clearChat} disabled={loading || messages.length === 0}>
			Очистить чат
		</button>
	</div>

	<label style="margin-top: 12px;">
		System prompt
		<input type="text" bind:value={systemPrompt} />
	</label>
</div>

<div class="card chat-box">
	{#if messages.length === 0}
		<p class="mut">Отправьте сообщение ниже, чтобы протестировать инференс через ноды Cordova.</p>
	{:else}
		<div class="messages">
			{#each messages as msg}
				<div class="msg {msg.role}">
					<div class="role">{msg.role === 'user' ? 'Вы' : 'Ассистент'}</div>
					<pre class="bubble">{msg.content || (loading ? '…' : '')}</pre>
				</div>
			{/each}
		</div>
	{/if}

	{#if lastUsage}
		<p class="mut" style="margin-top: 10px;">
			Токены: {lastUsage.prompt_tokens} вход + {lastUsage.completion_tokens} выход =
			{lastUsage.total_tokens} всего · Задержка: {lastUsage.latency_ms} мс
		</p>
	{/if}

	<form class="row" onsubmit={sendMessage} style="margin-top: 12px;">
		<textarea
			bind:value={promptInput}
			rows="3"
			placeholder="Введите запрос к модели..."
			style="flex: 1; min-width: 240px; font: inherit; padding: 8px 10px; border-radius: 6px; border: 1px solid var(--bd); background: #0e1218; color: var(--tx);"
		></textarea>
		{#if loading}
			<button type="button" class="del" onclick={stopGeneration}>Остановить</button>
		{:else}
			<button type="submit" disabled={!promptInput.trim()}>Отправить</button>
		{/if}
	</form>
</div>

<style>
	.messages {
		display: flex;
		flex-direction: column;
		gap: 10px;
		max-height: 460px;
		overflow-y: auto;
	}
	.msg .role {
		font-size: 12px;
		color: var(--mut);
		margin-bottom: 3px;
	}
	.msg.user .bubble {
		border-left: 3px solid var(--ac);
	}
	.msg.assistant .bubble {
		border-left: 3px solid var(--ok);
	}
	.bubble {
		margin: 0;
		white-space: pre-wrap;
		word-break: break-word;
	}
</style>
