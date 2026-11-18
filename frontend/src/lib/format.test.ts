import { describe, expect, it } from 'vitest';
import { formatBytes, formatXMR } from './format';

describe('formatXMR', () => {
	it('formats integer piconero accurately without float loss', () => {
		expect(formatXMR('1000000000000')).toBe('1.000000 XMR');
		expect(formatXMR('150000000000')).toBe('0.150000 XMR');
		expect(formatXMR('-500000000')).toBe('-0.000500 XMR');
		expect(formatXMR('0')).toBe('0.000000 XMR');
		expect(formatXMR(null)).toBe('0.000000 XMR');
	});

	it('formats byte sizes', () => {
		expect(formatBytes(1024 * 1024 * 1024)).toBe('1.0 GiB');
	});
});
