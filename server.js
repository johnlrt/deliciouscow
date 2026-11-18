// Mini-Cordova: примитивный координатор инференса (OpenAI-совместимый API + ноды + баланс).
// Без зависимостей. Node 18+. Запуск: node server.js
const http = require('http'), crypto = require('crypto'), fs = require('fs');
const PORT = +process.env.PORT || 8080, DB = process.env.DB || 'db.json';
const PRICE_PER_1K = 100, OWNER_SHARE = 0.8, MODELS = (process.env.MODELS || 'tiny-chat').split(',');
const TIMEOUT_MS = 60000;

let db = { users: {}, sessions: {}, keys: {}, nodes: {}, usage: [] };
try { db = JSON.parse(fs.readFileSync(DB)); } catch {}
const save = () => fs.writeFileSync(DB, JSON.stringify(db));
const rnd = (p) => p + crypto.randomBytes(24).toString('hex');
const hash = (s) => crypto.createHash('sha256').update(s).digest('hex');
const tokens = (s) => Math.ceil((s || '').length / 4);

const queue = [];            // ожидающие задания
const jobs = new Map();      // id -> {res, stream, model, user, keyId, reserved, text, inTok, waiters}
const pollers = [];          // ожидающие ноды {nodeId, res}

const body = (req) => new Promise((ok) => { let b = ''; req.on('data', d => b += d); req.on('end', () => { try { ok(JSON.parse(b || '{}')); } catch { ok({}); } }); });
const send = (res, code, obj) => { res.writeHead(code, { 'content-type': 'application/json', 'cache-control': 'no-store' }); res.end(JSON.stringify(obj)); };
const err = (res, code, type, msg) => send(res, code, { error: { message: msg, type, code: type } });
const bearer = (req) => (req.headers.authorization || '').replace(/^Bearer /, '');
const sessionUser = (req) => db.sessions[hash(bearer(req))];
const keyOf = (req) => db.keys[hash(bearer(req))];
const nodeOf = (req) => Object.values(db.nodes).find(n => n.secret === hash(bearer(req)));

function dispatch() {
  while (queue.length && pollers.length) {
    const job = queue.shift(), p = pollers.shift();
    if (!jobs.has(job.id)) continue;
    clearTimeout(p.timer);
    jobs.get(job.id).nodeId = p.nodeId;
    send(p.res, 200, { id: job.id, model: job.model, messages: job.messages, max_tokens: job.max_tokens });
  }
}

function finish(id, failed) {
  const j = jobs.get(id); if (!j) return;
  jobs.delete(id); clearTimeout(j.timer);
  const u = db.users[j.user], outTok = tokens(j.text);
  const cost = failed ? 0 : Math.min(j.reserved, Math.ceil((j.inTok + outTok) * PRICE_PER_1K / 1000));
  u.balance += j.reserved - cost;                       // возврат резерва
  if (!failed && j.nodeId && db.nodes[j.nodeId]) db.users[db.nodes[j.nodeId].owner].earnings += Math.floor(cost * OWNER_SHARE);
  db.usage.push({ id, user: j.user, model: j.model, in: j.inTok, out: outTok, cost, ok: !failed, at: Date.now() });
  save();
}

async function chat(req, res) {
  const key = keyOf(req); if (!key) return err(res, 401, 'authentication_error', 'bad api key');
  key.last = Date.now();
  const b = await body(req);
  if (!MODELS.includes(b.model)) return err(res, 404, 'model_not_found', 'unknown model');
  if (!Array.isArray(b.messages) || !b.messages.length) return err(res, 400, 'invalid_request', 'messages required');
  if (b.tools || b.functions) return err(res, 400, 'unsupported_parameter', 'tools not supported');
  const user = db.users[key.user], max = Math.min(+b.max_tokens || 256, 2048);
  const inTok = tokens(b.messages.map(m => m.content).join(' '));
  const reserve = Math.ceil((inTok + max) * PRICE_PER_1K / 1000);
  if (user.balance < reserve) return err(res, 429, 'insufficient_funds', 'top up balance');
  if (!Object.values(db.nodes).some(n => Date.now() - (n.seen || 0) < 30000)) { res.setHeader('retry-after', 2); return err(res, 503, 'no_capacity', 'no nodes online'); }
  user.balance -= reserve; save();
  const id = rnd('chatcmpl-').slice(0, 20);
  const job = { id, model: b.model, messages: b.messages, max_tokens: max };
  const j = { res, stream: !!b.stream, model: b.model, user: key.user, reserved: reserve, text: '', inTok };
  j.timer = setTimeout(() => { if (jobs.has(id)) { j.stream ? res.end() : err(res, 503, 'timeout', 'node timeout'); finish(id, true); } }, TIMEOUT_MS);
  jobs.set(id, j); queue.push(job);
  if (j.stream) res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store' });
  res.on('close', () => { if (jobs.has(id)) finish(id, true); });
  dispatch();
}

const chunk = (j, id, delta, fin) => 'data: ' + JSON.stringify({ id, object: 'chat.completion.chunk', model: j.model, choices: [{ index: 0, delta: fin ? {} : { content: delta }, finish_reason: fin ? 'stop' : null }] }) + '\n\n';

async function handle(req, res) {
  const url = new URL(req.url.replace(/^\/+/, '/'), 'http://x'), path = url.pathname, m = req.method;
  if (m === 'GET' && !path.startsWith('/api/') && !path.startsWith('/v1/') && !path.startsWith('/node/') && path !== '/health') {
    const f = path === '/' ? 'index.html' : path.replace(/[^a-zA-Z0-9._-]/g, '');
    try { const ext = f.split('.').pop(); res.writeHead(200, { 'content-type': { html: 'text/html', js: 'text/javascript', css: 'text/css' }[ext] || 'text/plain; charset=utf-8' }); return res.end(fs.readFileSync(__dirname + '/public/' + f)); } catch { return err(res, 404, 'not_found', 'no route'); }
  }
  if (m === 'GET' && path === '/health') return send(res, 200, { ok: true });
  if (m === 'POST' && path === '/api/register') {
    const b = await body(req);
    if (!b.username || !b.password || b.password.length < 8 || db.users[b.username]) return err(res, 400, 'invalid_request', 'bad username/password or taken');
    const salt = crypto.randomBytes(16).toString('hex');
    db.users[b.username] = { salt, pw: crypto.scryptSync(b.password, salt, 32).toString('hex'), balance: 0, earnings: 0 };
    const tok = rnd('ses_'); db.sessions[hash(tok)] = b.username; save();
    return send(res, 201, { session: tok });
  }
  if (m === 'POST' && path === '/api/login') {
    const b = await body(req), u = db.users[b.username];
    if (!u || crypto.scryptSync(b.password || '', u.salt, 32).toString('hex') !== u.pw) return err(res, 401, 'authentication_error', 'invalid credentials');
    const tok = rnd('ses_'); db.sessions[hash(tok)] = b.username; save();
    return send(res, 200, { session: tok });
  }
  if (path === '/v1/models') return send(res, 200, { object: 'list', data: MODELS.map(id => ({ id, object: 'model' })) });
  if (m === 'POST' && path === '/v1/chat/completions') return chat(req, res);

  // --- ноды ---
  if (path.startsWith('/node/')) {
    const n = nodeOf(req); if (!n) return err(res, 401, 'authentication_error', 'bad node secret');
    n.seen = Date.now();
    if (m === 'GET' && path === '/node/poll') {
      const p = { nodeId: n.id, res }; p.timer = setTimeout(() => { const i = pollers.indexOf(p); if (i >= 0) pollers.splice(i, 1); send(res, 204, {}); }, 25000);
      res.on('close', () => { const i = pollers.indexOf(p); if (i >= 0) { pollers.splice(i, 1); clearTimeout(p.timer); } });
      pollers.push(p); return dispatch();
    }
    const mm = path.match(/^\/node\/chunk\/(.+)$/);
    if (m === 'POST' && mm) {
      const j = jobs.get(mm[1]), b = await body(req);
      if (!j || j.nodeId !== n.id) return err(res, 404, 'not_found', 'job gone');
      if (b.delta) { j.text += b.delta; if (j.stream) j.res.write(chunk(j, mm[1], b.delta)); }
      if (b.done) {
        if (j.stream) { j.res.write(chunk(j, mm[1], '', true) + 'data: [DONE]\n\n'); j.res.end(); }
        else { const o = tokens(j.text); send(j.res, 200, { id: mm[1], object: 'chat.completion', model: j.model, choices: [{ index: 0, message: { role: 'assistant', content: j.text }, finish_reason: 'stop' }], usage: { prompt_tokens: j.inTok, completion_tokens: o, total_tokens: j.inTok + o } }); }
        finish(mm[1], false);
      }
      return send(res, 200, { ok: true });
    }
    return err(res, 404, 'not_found', 'no route');
  }

  // --- аккаунт (сессия) ---
  const user = sessionUser(req);
  if (!path.startsWith('/api/') ) return err(res, 404, 'not_found', 'no route');
  if (!user) return err(res, 401, 'authentication_error', 'login required');
  const u = db.users[user];
  if (m === 'GET' && path === '/api/balance') return send(res, 200, { balance: u.balance, earnings: u.earnings });
  if (m === 'POST' && path === '/api/topup') { const b = await body(req); u.balance += Math.max(0, Math.floor(+b.amount || 0)); save(); return send(res, 200, { balance: u.balance }); } // ФЕЙК-депозит вместо XMR
  if (m === 'POST' && path === '/api/payout') { const e = u.earnings; u.earnings = 0; u.balance += 0; save(); return send(res, 200, { paid_out: e, note: 'stub: no real payment' }); }
  if (m === 'POST' && path === '/api/keys') { const s = rnd('cvk_'); const b = await body(req); db.keys[hash(s)] = { id: s.slice(0, 12), name: String(b.name || 'key').slice(0, 40), user, created: Date.now(), last: null }; save(); return send(res, 201, { secret: s, id: s.slice(0, 12) }); }
  if (m === 'GET' && path === '/api/session') return send(res, 200, { username: user });
  if (m === 'POST' && path === '/api/logout') { delete db.sessions[hash(bearer(req))]; save(); return send(res, 200, { ok: true }); }
  if (m === 'GET' && path === '/api/keys') return send(res, 200, Object.values(db.keys).filter(k => k.user === user).map(k => ({ id: k.id, name: k.name, created: k.created, last: k.last })));
  let mk = path.match(/^\/api\/keys\/(.+)$/);
  if (m === 'DELETE' && mk) { for (const h in db.keys) if (db.keys[h].user === user && db.keys[h].id === mk[1]) delete db.keys[h]; save(); return send(res, 200, { ok: true }); }
  mk = path.match(/^\/api\/nodes\/(.+)$/);
  if (m === 'DELETE' && mk) { if (db.nodes[mk[1]]?.owner === user) delete db.nodes[mk[1]]; save(); return send(res, 200, { ok: true }); }
  if (m === 'POST' && path === '/api/nodes') { const id = rnd('n_').slice(0, 10), s = rnd('nsk_'); const b = await body(req); db.nodes[id] = { id, name: String(b.name || id).slice(0, 40), owner: user, secret: hash(s) }; save(); return send(res, 201, { node_id: id, node_secret: s }); }
  if (m === 'GET' && path === '/api/nodes') return send(res, 200, Object.values(db.nodes).filter(n => n.owner === user).map(n => ({ id: n.id, name: n.name || n.id, online: Date.now() - (n.seen || 0) < 30000, seen: n.seen || null })));
  if (m === 'GET' && path === '/api/usage') return send(res, 200, db.usage.filter(x => x.user === user).slice(-500).reverse());
  err(res, 404, 'not_found', 'no route');
}

http.createServer((req, res) => handle(req, res).catch(e => { console.error(e); if (!res.headersSent) err(res, 500, 'server_error', 'internal'); else res.end(); })).listen(PORT, () => console.log('mini-cordova on :' + PORT));
