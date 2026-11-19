#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const ROOT = path.resolve(__dirname, '..');

const OPENAPI_PATH = path.join(ROOT, 'contracts', 'openapi.yaml');
const WS_SCHEMA_PATH = path.join(ROOT, 'contracts', 'ws', 'v1.schema.json');
const FIXTURES_INDEX_PATH = path.join(ROOT, 'contracts', 'fixtures', 'index.json');
const GENERATED_TS_PATH = path.join(ROOT, 'frontend', 'src', 'lib', 'generated', 'contracts.ts');

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const DATE_TIME_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const URI_RE = /^[a-zA-Z][a-zA-Z0-9+.-]*:\/\/\S+$/;

function readJSON(filePath) {
	return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

function resolvePointer(doc, pointer) {
	if (!pointer || pointer === '#') return doc;
	const clean = pointer.startsWith('#/') ? pointer.slice(2) : pointer.replace(/^#/, '');
	if (!clean) return doc;
	let cur = doc;
	for (const rawPart of clean.split('/')) {
		const part = rawPart.replace(/~1/g, '/').replace(/~0/g, '~');
		if (cur === null || typeof cur !== 'object' || !(part in cur)) {
			throw new Error(`Unresolved JSON pointer: ${pointer}`);
		}
		cur = cur[part];
	}
	return cur;
}

function deepEqual(a, b) {
	if (a === b) return true;
	if (typeof a !== typeof b || typeof a !== 'object' || a === null || b === null) return false;
	if (Array.isArray(a) !== Array.isArray(b)) return false;
	if (Array.isArray(a)) {
		if (a.length !== b.length) return false;
		return a.every((v, i) => deepEqual(v, b[i]));
	}
	const ka = Object.keys(a);
	const kb = Object.keys(b);
	if (ka.length !== kb.length) return false;
	return ka.every((k) => Object.prototype.hasOwnProperty.call(b, k) && deepEqual(a[k], b[k]));
}

function matchesType(val, expectedType) {
	switch (expectedType) {
		case 'null':
			return val === null;
		case 'boolean':
			return typeof val === 'boolean';
		case 'string':
			return typeof val === 'string';
		case 'number':
			return typeof val === 'number' && Number.isFinite(val);
		case 'integer':
			return typeof val === 'number' && Number.isInteger(val);
		case 'array':
			return Array.isArray(val);
		case 'object':
			return val !== null && typeof val === 'object' && !Array.isArray(val);
		default:
			return false;
	}
}

function matchesFormat(val, fmt) {
	if (typeof val !== 'string') return true;
	switch (fmt) {
		case 'uuid':
			return UUID_RE.test(val);
		case 'date-time':
			return DATE_TIME_RE.test(val) && !Number.isNaN(Date.parse(val));
		case 'uri':
			return URI_RE.test(val);
		default:
			return true;
	}
}

function validateValue(val, schema, rootDoc, loc = '$') {
	const errors = [];
	if (!schema || typeof schema !== 'object') return errors;

	if (schema.$ref) {
		const target = resolvePointer(rootDoc, schema.$ref);
		return validateValue(val, target, rootDoc, loc);
	}

	if ('const' in schema && !deepEqual(val, schema.const)) {
		errors.push(`${loc}: expected const ${JSON.stringify(schema.const)}`);
	}

	if (schema.enum && !schema.enum.some((e) => deepEqual(val, e))) {
		errors.push(`${loc}: value not in enum ${JSON.stringify(schema.enum)}`);
	}

	if (schema.type !== undefined) {
		const types = Array.isArray(schema.type) ? schema.type : [schema.type];
		if (!types.some((t) => matchesType(val, t))) {
			errors.push(`${loc}: expected type ${types.join('|')}, got ${val === null ? 'null' : Array.isArray(val) ? 'array' : typeof val}`);
			return errors;
		}
	}

	if (typeof val === 'string') {
		if (schema.minLength !== undefined && val.length < schema.minLength) {
			errors.push(`${loc}: length ${val.length} < minLength ${schema.minLength}`);
		}
		if (schema.maxLength !== undefined && val.length > schema.maxLength) {
			errors.push(`${loc}: length ${val.length} > maxLength ${schema.maxLength}`);
		}
		if (schema.pattern !== undefined && !new RegExp(schema.pattern).test(val)) {
			errors.push(`${loc}: does not match pattern ${schema.pattern}`);
		}
		if (schema.format !== undefined && !matchesFormat(val, schema.format)) {
			errors.push(`${loc}: invalid format ${schema.format}`);
		}
	}

	if (typeof val === 'number') {
		if (schema.minimum !== undefined && val < schema.minimum) {
			errors.push(`${loc}: ${val} < minimum ${schema.minimum}`);
		}
		if (schema.maximum !== undefined && val > schema.maximum) {
			errors.push(`${loc}: ${val} > maximum ${schema.maximum}`);
		}
		if (schema.exclusiveMinimum !== undefined && val <= schema.exclusiveMinimum) {
			errors.push(`${loc}: ${val} <= exclusiveMinimum ${schema.exclusiveMinimum}`);
		}
		if (schema.exclusiveMaximum !== undefined && val >= schema.exclusiveMaximum) {
			errors.push(`${loc}: ${val} >= exclusiveMaximum ${schema.exclusiveMaximum}`);
		}
	}

	if (Array.isArray(val)) {
		if (schema.minItems !== undefined && val.length < schema.minItems) {
			errors.push(`${loc}: array length ${val.length} < minItems ${schema.minItems}`);
		}
		if (schema.maxItems !== undefined && val.length > schema.maxItems) {
			errors.push(`${loc}: array length ${val.length} > maxItems ${schema.maxItems}`);
		}
		if (schema.uniqueItems) {
			for (let i = 0; i < val.length; i++) {
				for (let j = i + 1; j < val.length; j++) {
					if (deepEqual(val[i], val[j])) {
						errors.push(`${loc}: duplicate item at indices ${i} and ${j}`);
						break;
					}
				}
			}
		}
		if (schema.items) {
			val.forEach((item, idx) => {
				errors.push(...validateValue(item, schema.items, rootDoc, `${loc}[${idx}]`));
			});
		}
		if (schema.contains) {
			const anyMatch = val.some((item) => validateValue(item, schema.contains, rootDoc, loc).length === 0);
			if (!anyMatch) {
				errors.push(`${loc}: array does not match contains schema`);
			}
		}
	}

	if (val !== null && typeof val === 'object' && !Array.isArray(val)) {
		if (schema.required) {
			for (const reqKey of schema.required) {
				if (!Object.prototype.hasOwnProperty.call(val, reqKey)) {
					errors.push(`${loc}: missing required property "${reqKey}"`);
				}
			}
		}
		const props = schema.properties || {};
		for (const [k, v] of Object.entries(val)) {
			if (Object.prototype.hasOwnProperty.call(props, k)) {
				errors.push(...validateValue(v, props[k], rootDoc, `${loc}.${k}`));
			} else if (schema.additionalProperties === false) {
				errors.push(`${loc}: unexpected property "${k}"`);
			} else if (schema.additionalProperties && typeof schema.additionalProperties === 'object') {
				errors.push(...validateValue(v, schema.additionalProperties, rootDoc, `${loc}.${k}`));
			}
		}
	}

	if (schema.allOf) {
		for (const sub of schema.allOf) {
			errors.push(...validateValue(val, sub, rootDoc, loc));
		}
	}

	if (schema.anyOf) {
		const matched = schema.anyOf.some((sub) => validateValue(val, sub, rootDoc, loc).length === 0);
		if (!matched) {
			errors.push(`${loc}: failed anyOf`);
		}
	}

	if (schema.oneOf) {
		const matchCount = schema.oneOf.filter((sub) => validateValue(val, sub, rootDoc, loc).length === 0).length;
		if (matchCount !== 1) {
			errors.push(`${loc}: expected exactly 1 oneOf match, got ${matchCount}`);
		}
	}

	if (schema.not) {
		if (validateValue(val, schema.not, rootDoc, loc).length === 0) {
			errors.push(`${loc}: matched forbidden "not" schema`);
		}
	}

	if (schema.if) {
		const condOk = validateValue(val, schema.if, rootDoc, loc).length === 0;
		if (condOk && schema.then) {
			errors.push(...validateValue(val, schema.then, rootDoc, loc));
		} else if (!condOk && schema.else) {
			errors.push(...validateValue(val, schema.else, rootDoc, loc));
		}
	}

	return errors;
}

function schemaToTs(schema, prefix, indent = 0) {
	const pad = '\t'.repeat(indent);
	if (!schema || typeof schema !== 'object') return 'unknown';

	if (schema.$ref) {
		const name = schema.$ref.split('/').pop();
		return `${prefix}${name}`;
	}
	if ('const' in schema) {
		return JSON.stringify(schema.const);
	}
	if (schema.enum) {
		return schema.enum.map((v) => JSON.stringify(v)).join(' | ');
	}
	if (schema.anyOf) {
		return schema.anyOf.map((s) => schemaToTs(s, prefix, indent)).join(' | ');
	}
	if (schema.oneOf) {
		return schema.oneOf.map((s) => schemaToTs(s, prefix, indent)).join(' | ');
	}
	if (schema.allOf) {
		return schema.allOf.map((s) => schemaToTs(s, prefix, indent)).join(' & ');
	}

	if (Array.isArray(schema.type)) {
		return schema.type
			.map((t) => schemaToTs({ ...schema, type: t }, prefix, indent))
			.join(' | ');
	}

	switch (schema.type) {
		case 'null':
			return 'null';
		case 'boolean':
			return 'boolean';
		case 'string':
			return 'string';
		case 'integer':
		case 'number':
			return 'number';
		case 'array': {
			const itemTs = schema.items ? schemaToTs(schema.items, prefix, indent) : 'unknown';
			return itemTs.includes(' | ') || itemTs.includes(' & ') ? `(${itemTs})[]` : `${itemTs}[]`;
		}
		case 'object': {
			const props = schema.properties || {};
			const entries = Object.entries(props);
			if (entries.length === 0) {
				if (schema.additionalProperties && typeof schema.additionalProperties === 'object') {
					return `Record<string, ${schemaToTs(schema.additionalProperties, prefix, indent)}>`;
				}
				return 'Record<string, unknown>';
			}
			const req = new Set(schema.required || []);
			const lines = entries.map(([k, v]) => {
				const opt = req.has(k) ? '' : '?';
				const propKey = /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(k) ? k : JSON.stringify(k);
				return `${pad}\t${propKey}${opt}: ${schemaToTs(v, prefix, indent + 1)};`;
			});
			return `{\n${lines.join('\n')}\n${pad}}`;
		}
		default:
			return 'unknown';
	}
}

function renderGeneratedTypes() {
	const openapi = readJSON(OPENAPI_PATH);
	const wsSchema = readJSON(WS_SCHEMA_PATH);

	const out = [
		'// Code generated by scripts/contracts.mjs from contracts/openapi.yaml and contracts/ws/v1.schema.json.',
		'// DO NOT EDIT MANUALLY. Run `npm run contracts:gen` (or `node scripts/contracts.mjs generate`).',
		'',
		'// OpenAPI 3.1 component schemas',
	];

	const openapiSchemas = openapi.components?.schemas || {};
	for (const name of Object.keys(openapiSchemas).sort()) {
		const ts = schemaToTs(openapiSchemas[name], '', 0);
		if (ts.startsWith('{')) {
			out.push(`export interface ${name} ${ts}`);
		} else {
			out.push(`export type ${name} = ${ts};`);
		}
		out.push('');
	}

	out.push('// WebSocket v1 schema definitions');
	const wsDefs = wsSchema.$defs || {};
	for (const name of Object.keys(wsDefs).sort()) {
		const ts = schemaToTs(wsDefs[name], 'Ws', 0);
		if (ts.startsWith('{')) {
			out.push(`export interface Ws${name} ${ts}`);
		} else {
			out.push(`export type Ws${name} = ${ts};`);
		}
		out.push('');
	}

	return out.join('\n');
}

function cmdGenerate() {
	const rendered = renderGeneratedTypes();
	fs.mkdirSync(path.dirname(GENERATED_TS_PATH), { recursive: true });
	fs.writeFileSync(GENERATED_TS_PATH, rendered, 'utf8');
	console.log(`Generated ${path.relative(ROOT, GENERATED_TS_PATH)}`);
}

function cmdCheck() {
	let failed = false;

	// 1. Check generated TypeScript drift
	const expected = renderGeneratedTypes();
	if (!fs.existsSync(GENERATED_TS_PATH)) {
		console.error(`FAIL: missing generated file ${path.relative(ROOT, GENERATED_TS_PATH)} (run: node scripts/contracts.mjs generate)`);
		failed = true;
	} else {
		const actual = fs.readFileSync(GENERATED_TS_PATH, 'utf8');
		if (actual !== expected) {
			console.error(`FAIL: generated TypeScript drift in ${path.relative(ROOT, GENERATED_TS_PATH)} (run: node scripts/contracts.mjs generate)`);
			failed = true;
		} else {
			console.log(`PASS: generated TypeScript types in sync (${path.relative(ROOT, GENERATED_TS_PATH)})`);
		}
	}

	// 2. Validate all fixtures in contracts/fixtures/index.json
	const schemaCache = new Map();
	const loadSchemaDoc = (relPath) => {
		if (!schemaCache.has(relPath)) {
			schemaCache.set(relPath, readJSON(path.join(ROOT, 'contracts', relPath)));
		}
		return schemaCache.get(relPath);
	};

	const index = readJSON(FIXTURES_INDEX_PATH);
	let passedCount = 0;
	for (const entry of index) {
		const [schemaFile, pointer] = entry.schema.split('#');
		const rootDoc = loadSchemaDoc(schemaFile);
		const targetSchema = resolvePointer(rootDoc, pointer ? `#${pointer}` : '#');
		const fixturePath = path.join(ROOT, 'contracts', 'fixtures', entry.file);
		const fixtureData = readJSON(fixturePath);
		const errs = validateValue(fixtureData, targetSchema, rootDoc);
		const isValid = errs.length === 0;
		if (isValid !== entry.valid) {
			failed = true;
			console.error(
				`FAIL: fixture ${entry.file} against ${entry.schema}: expected valid=${entry.valid}, got valid=${isValid}`
			);
			if (errs.length > 0) {
				console.error(`  errors: ${errs.slice(0, 5).join('; ')}`);
			}
		} else {
			passedCount++;
		}
	}

	if (failed) {
		process.exit(1);
	}
	console.log(`PASS: contracts check (${passedCount}/${index.length} fixtures validated)`);
}

const cmd = process.argv[2] || 'check';
if (cmd === 'generate') {
	cmdGenerate();
} else if (cmd === 'check') {
	cmdCheck();
} else {
	console.error(`Unknown command: ${cmd}. Use "generate" or "check".`);
	process.exit(2);
}
