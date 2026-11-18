import { browser } from '$app/environment';

export type LiveTopic = 'nodes' | 'usage' | 'billing';

export function connectLive(onInvalidate: (topic: LiveTopic, resourceId: string) => void): () => void {
	if (!browser || import.meta.env.VITE_MOCK === '1') {
		return () => {};
	}
	let ws: WebSocket | null = null;
	let closed = false;
	let timer: ReturnType<typeof setTimeout> | null = null;
	const pending = new Map<LiveTopic, ReturnType<typeof setTimeout>>();

	const schedule = (topic: LiveTopic, resourceId: string) => {
		const prev = pending.get(topic);
		if (prev) clearTimeout(prev);
		pending.set(
			topic,
			setTimeout(() => {
				pending.delete(topic);
				onInvalidate(topic, resourceId);
			}, 250)
		);
	};

	const connect = () => {
		if (closed) return;
		const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
		ws = new WebSocket(`${proto}//${location.host}/api/live`);
		ws.onopen = () => {
			ws?.send(
				JSON.stringify({
					v: 1,
					type: 'live.subscribe',
					message_id: crypto.randomUUID(),
					request_id: null,
					session_id: null,
					seq: 1,
					sent_at: new Date().toISOString(),
					payload: { topics: ['nodes', 'usage', 'billing'], last_event_id: null },
					error: null
				})
			);
		};
		ws.onmessage = (ev) => {
			try {
				const msg = JSON.parse(ev.data);
				if (msg.type === 'live.event' && msg.payload?.topic) {
					schedule(msg.payload.topic as LiveTopic, msg.payload.resource_id || '');
				}
			} catch {
				// ignore malformed frame
			}
		};
		ws.onclose = () => {
			if (!closed) {
				timer = setTimeout(connect, 3000);
			}
		};
	};

	connect();
	return () => {
		closed = true;
		if (timer) clearTimeout(timer);
		for (const t of pending.values()) clearTimeout(t);
		ws?.close();
	};
}
