import type {
	Analytics,
	ApiKey,
	Balance,
	Deposit,
	DepositAddress,
	LedgerView,
	Model,
	Node,
	Page,
	Payout,
	PayoutSettings,
	RegisterResponse,
	SessionView,
	UsageRow
} from './types';

export class ApiError extends Error {
	constructor(
		public status: number,
		public code: string,
		message: string,
		public requestId: string | null = null
	) {
		super(message);
	}
}

const SAFE_ERROR_MESSAGES: Record<string, string> = {
	invalid_request: 'Проверьте корректность введённых данных.',
	unsupported_parameter: 'Запрошенный параметр не поддерживается.',
	unauthenticated: 'Требуется вход в аккаунт.',
	forbidden: 'Недостаточно прав для этого действия.',
	csrf_failed: 'Сессия устарела. Обновите страницу и повторите.',
	not_found: 'Ресурс не найден.',
	conflict: 'Конфликт состояния. Повторите попытку.',
	insufficient_funds: 'Недостаточно средств на балансе.',
	spend_cap_exceeded: 'Превышен дневной лимит расхода ключа.',
	rate_limited: 'Слишком много запросов. Попробуйте чуть позже.',
	verification_required: 'Требуется подтверждение паролем и TOTP.',
	enrollment_pending: 'Ожидание привязки ноды владельцем.'
};

let currentCsrf = '';

export function setCsrfToken(token: string) {
	currentCsrf = token;
}

const USE_MOCK = import.meta.env.VITE_MOCK === '1';

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
	if (USE_MOCK) {
		return mockFetch<T>(path, init);
	}
	const method = (init.method || 'GET').toUpperCase();
	const headers: Record<string, string> = {
		'Content-Type': 'application/json',
		...(init.headers as Record<string, string> | undefined)
	};
	if (method !== 'GET' && method !== 'HEAD' && currentCsrf) {
		headers['X-CSRF-Token'] = currentCsrf;
	}
	const res = await fetch(path, {
		...init,
		credentials: 'include',
		headers
	});
	if (res.status === 204) {
		return undefined as T;
	}
	const data = await res.json().catch(() => ({}));
	if (!res.ok) {
		const code = data?.error?.code || 'unknown_error';
		const msg = SAFE_ERROR_MESSAGES[code] || data?.error?.message || `Ошибка ${res.status}`;
		throw new ApiError(res.status, code, msg, data?.request_id || null);
	}
	return data as T;
}

export const api = {
	getSession: async () => {
		const s = await request<SessionView>('/api/session');
		setCsrfToken(s.csrf_token);
		return s;
	},
	register: async (username: string, password: string, privacy_notice_version = 'v1') => {
		const s = await request<RegisterResponse>('/api/auth/register', {
			method: 'POST',
			body: JSON.stringify({ username, password, privacy_notice_version })
		});
		setCsrfToken(s.csrf_token);
		return s;
	},
	login: async (username: string, password: string, totp_code?: string) => {
		const s = await request<SessionView>('/api/auth/login', {
			method: 'POST',
			body: JSON.stringify({ username, password, ...(totp_code ? { totp_code } : {}) })
		});
		setCsrfToken(s.csrf_token);
		return s;
	},
	recover: async (username: string, recovery_code: string, new_password: string) => {
		const s = await request<SessionView>('/api/auth/recover', {
			method: 'POST',
			body: JSON.stringify({ username, recovery_code, new_password })
		});
		setCsrfToken(s.csrf_token);
		return s;
	},
	logout: async () => {
		await request<void>('/api/auth/logout', { method: 'POST', body: '{}' });
		setCsrfToken('');
	},
	setupTOTP: (password: string, totp_code?: string) =>
		request<{ setup_id: string; otpauth_uri: string; expires_at: string }>('/api/auth/totp/setup', {
			method: 'POST',
			body: JSON.stringify({ password, ...(totp_code ? { totp_code } : {}) })
		}),
	confirmTOTP: (setup_id: string, code: string) =>
		request<void>('/api/auth/totp/confirm', {
			method: 'POST',
			body: JSON.stringify({ setup_id, code })
		}),
	stepUp: (password: string, totp_code: string) =>
		request<{ step_up_token: string; expires_at: string }>('/api/auth/step-up', {
			method: 'POST',
			body: JSON.stringify({ password, totp_code })
		}),

	listKeys: () => request<Page<ApiKey>>('/api/keys'),
	createKey: (payload: {
		name: string;
		permissions: string[];
		model_ids: string[];
		daily_spend_cap_piconero: string | null;
	}) =>
		request<{ key: ApiKey; secret: string }>('/api/keys', {
			method: 'POST',
			body: JSON.stringify(payload)
		}),
	deleteKey: (id: string) => request<void>(`/api/keys/${encodeURIComponent(id)}`, { method: 'DELETE' }),

	listNodes: () => request<Page<Node>>('/api/nodes'),
	getNode: (id: string) => request<Node>(`/api/nodes/${encodeURIComponent(id)}`),
	linkNode: (user_code: string, name: string) =>
		request<Node>('/api/nodes/link', {
			method: 'POST',
			body: JSON.stringify({ user_code, name })
		}),
	renameNode: (id: string, name: string) =>
		request<Node>(`/api/nodes/${encodeURIComponent(id)}`, {
			method: 'PATCH',
			body: JSON.stringify({ name })
		}),
	revokeNode: (id: string) =>
		request<void>(`/api/nodes/${encodeURIComponent(id)}/revoke`, {
			method: 'POST',
			body: '{}'
		}),

	getDepositAddress: () => request<DepositAddress>('/api/billing/address'),
	getBalance: () => request<Balance>('/api/billing/balance'),
	listDeposits: () => request<Page<Deposit>>('/api/billing/deposits'),
	listLedgerHistory: () => request<Page<LedgerView>>('/api/billing/history'),
	getPayoutSettings: () => request<PayoutSettings>('/api/billing/payout-settings'),
	updatePayoutSettings: (address: string, step_up_token: string) =>
		request<PayoutSettings>('/api/billing/payout-settings', {
			method: 'PUT',
			body: JSON.stringify({ address, step_up_token })
		}),
	listPayouts: () => request<Page<Payout>>('/api/billing/payouts'),
	devDeposit: (amount_piconero: string) =>
		request<{ deposit_id: string; txid: string; available_piconero: string }>('/api/dev/deposit', {
			method: 'POST',
			body: JSON.stringify({ amount_piconero })
		}),

	listUsage: (modelId = '') =>
		request<Page<UsageRow>>(`/api/usage${modelId ? `?model_id=${encodeURIComponent(modelId)}` : ''}`),
	getAnalytics: (bucket: 'hour' | 'day' = 'day') =>
		request<Analytics>(`/api/analytics?bucket=${bucket}`),
	listModels: () => request<{ object: 'list'; data: Model[] }>('/v1/models')
};

function mockFetch<T>(path: string, _init: RequestInit): Promise<T> {
	if (path === '/api/session') {
		return Promise.resolve({
			account_id: 'mock-acc-1',
			username: 'demo',
			csrf_token: 'mock-csrf',
			totp_enabled: false,
			privacy_notice_version: 'v1',
			session_expires_at: new Date(Date.now() + 3600e3).toISOString(),
			recovery_codes_remaining: 8
		} as T);
	}
	if (path === '/api/billing/balance') {
		return Promise.resolve({
			available_piconero: '150000000000',
			reserved_piconero: '0',
			earnings_pending_piconero: '25000000000',
			earnings_available_piconero: '50000000000',
			payout_reserved_piconero: '0',
			currency: 'XMR',
			updated_at: new Date().toISOString()
		} as T);
	}
	if (path === '/api/nodes') {
		return Promise.resolve({ data: [], next_cursor: null } as T);
	}
	if (path === '/api/keys') {
		return Promise.resolve({ data: [], next_cursor: null } as T);
	}
	if (path.startsWith('/api/usage')) {
		return Promise.resolve({ data: [], next_cursor: null } as T);
	}
	if (path.startsWith('/api/analytics')) {
		return Promise.resolve({
			from: new Date(Date.now() - 86400e3 * 7).toISOString(),
			to: new Date().toISOString(),
			bucket: 'day',
			buckets: []
		} as T);
	}
	return Promise.resolve({ data: [], next_cursor: null } as T);
}
