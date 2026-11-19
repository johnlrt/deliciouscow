export type Money = string; // piconero decimal string per C1

export interface APIErrorBody {
	message: string;
	type: string;
	param: string | null;
	code: string;
}

export interface APIErrorPayload {
	error: APIErrorBody;
	request_id: string;
}

export interface SessionView {
	account_id: string;
	username: string;
	csrf_token: string;
	totp_enabled: boolean;
	privacy_notice_version: string;
	session_expires_at: string;
	recovery_codes_remaining: number;
}

export interface RegisterResponse extends SessionView {
	recovery_codes: string[];
}

export interface ApiKey {
	id: string;
	name: string;
	prefix: string;
	permissions: string[];
	model_ids: string[];
	daily_spend_cap_piconero: Money | null;
	created_at: string;
	last_used_at: string | null;
	revoked_at: string | null;
}

export interface Hardware {
	cpu_model: string;
	cpu_cores: number;
	ram_bytes: number;
	gpu_model?: string;
	vram_bytes?: number;
	os: string;
	arch: string;
}

export interface NodeModel {
	model_id: string;
	state: 'loaded' | 'on_disk' | 'absent';
	artifact_sha256: string | null;
	active_slots: number;
	max_slots: number;
}

export interface Node {
	id: string;
	name: string;
	status: 'online' | 'offline' | 'draining' | 'revoked';
	node_version: string;
	hardware: Hardware;
	models: NodeModel[];
	rank_score: number;
	throughput_tps: number | null;
	active_slots: number;
	max_slots: number;
	last_seen_at: string | null;
	created_at: string;
	revoked_at: string | null;
}

export interface Model {
	id: string;
	name: string;
	context_length: number;
	max_output_tokens: number;
	input_price_per_1m_piconero: Money;
	output_price_per_1m_piconero: Money;
	owner_share_bps: number;
	availability: 'available' | 'warming' | 'unavailable';
}

export interface Balance {
	available_piconero: Money;
	reserved_piconero: Money;
	earnings_pending_piconero: Money;
	earnings_available_piconero: Money;
	payout_reserved_piconero: Money;
	currency: 'XMR';
	updated_at: string;
}

export interface DepositAddress {
	address: string;
	network: 'stagenet' | 'mainnet';
	confirmations_required: number;
	uri: string;
	created_at: string;
}

export interface Deposit {
	id: string;
	txid: string;
	amount_piconero: Money;
	confirmations: number;
	status: 'pending' | 'credited' | 'reversed' | 'locked';
	observed_at: string;
	credited_at: string | null;
}

export interface LedgerView {
	id: string;
	kind: string;
	request_id: string | null;
	amount_piconero: string;
	balance_after_piconero: Money;
	created_at: string;
	reference_id: string;
}

export interface PayoutSettings {
	address: string | null;
	network: 'stagenet' | 'mainnet';
	minimum_piconero: Money;
	changed_at: string | null;
	eligible_after: string | null;
}

export interface Payout {
	id: string;
	amount_piconero: Money;
	status: 'queued' | 'prepared' | 'relaying' | 'broadcast' | 'confirmed' | 'unknown' | 'failed';
	txids: string[];
	created_at: string;
	updated_at: string;
	hold_reason: string | null;
}

export type { UsageRow } from './generated/contracts';

export interface AnalyticsBucket {
	start: string;
	requests: number;
	input_tokens: number;
	output_tokens: number;
	spend_piconero: Money;
	p50_latency_ms: number | null;
	p95_latency_ms: number | null;
	successes: number;
	failures: number;
}

export interface Analytics {
	from: string;
	to: string;
	bucket: 'hour' | 'day';
	buckets: AnalyticsBucket[];
}

export interface Page<T> {
	data: T[];
	next_cursor: string | null;
}
