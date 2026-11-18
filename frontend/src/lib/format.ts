const PICONERO_PER_XMR = 1_000_000_000_000n;

/**
 * Formats integer piconero string into human-readable XMR without float precision loss.
 */
export function formatXMR(piconero: string | null | undefined, decimals = 6): string {
	if (!piconero) return '0.000000 XMR';
	const raw = piconero.trim();
	const neg = raw.startsWith('-');
	const absStr = neg || raw.startsWith('+') ? raw.slice(1) : raw;
	if (!/^\d+$/.test(absStr)) return '—';
	const val = BigInt(absStr);
	const whole = val / PICONERO_PER_XMR;
	const frac = val % PICONERO_PER_XMR;
	const fracPadded = frac.toString().padStart(12, '0').slice(0, decimals);
	return `${neg ? '-' : ''}${whole}.${fracPadded} XMR`;
}

export function formatBytes(bytes: number | null | undefined): string {
	if (!bytes || bytes <= 0) return '—';
	const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	let idx = 0;
	let v = bytes;
	while (v >= 1024 && idx < units.length - 1) {
		v /= 1024;
		idx++;
	}
	return `${v.toFixed(1)} ${units[idx]}`;
}

export function formatUTC(iso: string | null | undefined): string {
	if (!iso) return '—';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '—';
	return d.toISOString().replace('T', ' ').slice(0, 19) + ' UTC';
}
