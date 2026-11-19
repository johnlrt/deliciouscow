import { describe, expect, it } from 'vitest';
import type { UsageRow } from './types';
import type { UsageRow as GeneratedUsageRow, WsInferenceStartPayload } from './generated/contracts';

describe('generated contract types', () => {
	it('UsageRow re-exported from types matches generated OpenAPI UsageRow with billing fields', () => {
		const nonBillable: UsageRow = {
			request_id: '00000000-0000-4000-8000-000000000002',
			api_key_id: '00000000-0000-4000-8000-000000000003',
			model_id: 'cordova-chat-small-v1',
			status: 'settled',
			input_tokens: 10,
			output_tokens: 20,
			charged_piconero: '0',
			billable: false,
			tokenization_verified: false,
			billing_note: 'non_billable_unverified_tokenization',
			latency_ms: 42,
			created_at: '2026-10-09T10:00:00Z',
			finished_at: '2026-10-09T10:00:01Z'
		};
		const billable: GeneratedUsageRow = {
			...nonBillable,
			billable: true,
			tokenization_verified: true,
			billing_note: null
		};
		expect(nonBillable.billable).toBe(false);
		expect(nonBillable.tokenization_verified).toBe(false);
		expect(nonBillable.billing_note).toBe('non_billable_unverified_tokenization');
		expect(billable.billable).toBe(true);
		expect(billable.tokenization_verified).toBe(true);
		expect(billable.billing_note).toBeNull();
	});

	it('WsInferenceStartPayload requires boolean tokenization_verified', () => {
		const payload: WsInferenceStartPayload = {
			attempt_id: '7a841e6b-3a1e-4d58-9c61-0e34b72d9f33',
			lease_id: '3e29a7c0-7f24-4b66-8a21-6c5d4e3f2a44',
			model_id: 'llama-3-8b-instruct',
			catalog_version: '11111111-1111-4111-8111-111111111111',
			input_token_ids: [128000, 9906],
			input_tokens: 2,
			tokenization_verified: true,
			max_tokens: 128,
			parameters: {
				temperature: 0.7,
				top_p: 0.95,
				stop: [],
				seed: null
			},
			deadline_at: '2026-10-09T10:01:01Z',
			kind: 'user'
		};
		expect(typeof payload.tokenization_verified).toBe('boolean');
	});
});
