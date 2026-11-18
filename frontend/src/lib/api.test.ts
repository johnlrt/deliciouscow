import { describe, expect, it, vi } from 'vitest';
import { ApiError, api, setCsrfToken } from './api';

describe('api client', () => {
	it('sends X-CSRF-Token on mutations and maps C2 error codes', async () => {
		setCsrfToken('csrf-123');
		const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
			expect((init?.headers as Record<string, string>)['X-CSRF-Token']).toBe('csrf-123');
			return new Response(
				JSON.stringify({
					error: { code: 'insufficient_funds', message: 'raw', type: 'rate_limit_error', param: null },
					request_id: 'req-1'
				}),
				{ status: 429, headers: { 'content-type': 'application/json' } }
			);
		});
		vi.stubGlobal('fetch', fetchMock);

		await expect(api.devDeposit('100')).rejects.toThrow(ApiError);
		vi.unstubAllGlobals();
	});
});
