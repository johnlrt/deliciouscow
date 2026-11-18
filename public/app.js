// Фронтенд Mini-Cordova: SPA на чистом JS, hash-роутинг. Маршруты повторяют PLAN-front.md в упрощённом виде.
const $ = (s) => document.querySelector(s), app = $('#app');
let ses = localStorage.ses || '', me = null;
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const toast = (t) => { const e = $('#toast'); e.textContent = t; e.style.display = 'block'; setTimeout(() => e.style.display = 'none', 3500); };
async function api(path, method = 'GET', body) {
  const r = await fetch('/api' + path, { method, headers: { 'content-type': 'application/json', ...(ses && { authorization: 'Bearer ' + ses }) }, body: body && JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (r.status === 401 && ses) { ses = ''; delete localStorage.ses; me = null; location.hash = '#/login'; }
  if (!r.ok) throw new Error(j.error?.message || r.status);
  return j;
}
const date = (t) => t ? new Date(t).toLocaleString() : '—';
const fd = (e) => Object.fromEntries(new FormData(e.target));
const onSubmit = (id, fn) => $(id).onsubmit = async (e) => { e.preventDefault(); try { await fn(fd(e), e); } catch (x) { toast('Ошибка: ' + x.message); } };

const tabs = [['dashboard', 'Обзор'], ['dashboard/nodes', 'Ноды'], ['dashboard/keys', 'Ключи'], ['dashboard/billing', 'Баланс'], ['dashboard/analytics', 'Аналитика'], ['docs', 'Документация']];
function nav(route) {
  $('#nav').innerHTML = (me ? tabs.map(([r, n]) => `<a href="#/${r}" class="${route === r ? 'on' : ''}">${n}</a>`).join('') + `<a href="#" id="out">Выйти (${esc(me)})</a>`
    : `<a href="#/docs">Документация</a><a href="#/login">Вход</a><a href="#/register">Регистрация</a>`);
  if ($('#out')) $('#out').onclick = async (e) => { e.preventDefault(); await api('/logout', 'POST').catch(() => {}); ses = ''; delete localStorage.ses; me = null; location.hash = '#/'; };
}

const pages = {
  '': () => `<h1>Mini-Cordova</h1><p>Сеть сообщества для запуска LLM с OpenAI-совместимым API. Подключайте свои ноды, зарабатывайте на запросах, вызывайте модели по ключу.</p>
   <div class="card warn"><b>Приватность:</b> ноды сообщества видят ваши запросы открытым текстом. Не отправляйте секреты и персональные данные. Нет шифрования и проверки честности нод — это учебный софт.</div>
   <p><a href="#/register">Создать аккаунт</a> · <a href="#/login">Войти</a> · <a href="#/docs/api">Документация API</a></p>`,
  login() { return `<h1>Вход</h1><form id="f" class="col"><input name="username" placeholder="Логин" required autocomplete="username"><input name="password" type="password" placeholder="Пароль" required autocomplete="current-password"><button>Войти</button></form>`; },
  loginInit() { onSubmit('#f', async (d) => { const r = await api('/login', 'POST', d); ses = localStorage.ses = r.session; location.hash = '#/dashboard'; }); },
  register() { return `<h1>Регистрация</h1><form id="f" class="col"><input name="username" placeholder="Логин" required><input name="password" type="password" minlength="8" placeholder="Пароль (мин. 8)" required autocomplete="new-password">
   <label><input type="checkbox" required> Я понимаю, что ноды видят мои запросы открытым текстом</label><button>Создать</button></form>`; },
  registerInit() { onSubmit('#f', async (d) => { const r = await api('/register', 'POST', d); ses = localStorage.ses = r.session; location.hash = '#/dashboard'; }); },
  async dashboard() {
    const [b, n, u] = await Promise.all([api('/balance'), api('/nodes'), api('/usage')]);
    const on = n.filter(x => x.online).length, spent = u.reduce((a, x) => a + x.cost, 0);
    return `<h1>Обзор</h1><div class="grid">
     <div class="card"><div class="mut">Баланс</div><div class="big">${b.balance}</div><a href="#/dashboard/billing">Пополнить</a></div>
     <div class="card"><div class="mut">Заработок</div><div class="big">${b.earnings}</div></div>
     <div class="card"><div class="mut">Ноды онлайн</div><div class="big">${on} / ${n.length}</div><a href="#/dashboard/nodes">Управление</a></div>
     <div class="card"><div class="mut">Запросов / потрачено</div><div class="big">${u.length} / ${spent}</div><a href="#/dashboard/analytics">Аналитика</a></div></div>
     ${!n.length && !b.balance ? '<div class="card">Следующий шаг: <a href="#/dashboard/billing">пополните баланс</a>, <a href="#/dashboard/keys">создайте ключ</a> или <a href="#/dashboard/nodes">добавьте ноду</a>.</div>' : ''}`;
  },
  async nodes() {
    const n = await api('/nodes');
    return `<h1>Ноды</h1><div class="card"><form id="f"><input name="name" placeholder="Название ноды" required><button>Добавить ноду</button></form><div id="secret"></div></div>
    <div class="card"><table><tr><th>Название</th><th>ID</th><th>Статус</th><th>Последний контакт</th><th></th></tr>${n.map(x => `<tr><td>${esc(x.name)}</td><td><code>${esc(x.id)}</code></td><td class="${x.online ? 'ok' : 'mut'}">${x.online ? 'онлайн' : 'офлайн'}</td><td>${date(x.seen)}</td><td><button class="del" data-id="${esc(x.id)}">Отозвать</button></td></tr>`).join('') || '<tr><td colspan=5 class="mut">Нет нод</td></tr>'}</table></div>`;
  },
  nodesInit() {
    onSubmit('#f', async (d) => { const r = await api('/nodes', 'POST', d); $('#secret').innerHTML = `<p class="warn card">Секрет показывается один раз. Запуск ноды:<pre>SERVER=${location.origin} NODE_SECRET=${esc(r.node_secret)} node node.js</pre></p>`; });
    document.querySelectorAll('button[data-id]').forEach(b => b.onclick = async () => { if (confirm('Отозвать ноду?')) { await api('/nodes/' + b.dataset.id, 'DELETE'); route(); } });
  },
  async keys() {
    const k = await api('/keys');
    return `<h1>API-ключи</h1><div class="card"><form id="f"><input name="name" placeholder="Название ключа" required><button>Создать ключ</button></form><div id="secret"></div></div>
    <div class="card"><table><tr><th>Название</th><th>Префикс</th><th>Создан</th><th>Использован</th><th></th></tr>${k.map(x => `<tr><td>${esc(x.name)}</td><td><code>${esc(x.id)}…</code></td><td>${date(x.created)}</td><td>${date(x.last)}</td><td><button class="del" data-id="${esc(x.id)}">Удалить</button></td></tr>`).join('') || '<tr><td colspan=5 class="mut">Нет ключей</td></tr>'}</table></div>`;
  },
  keysInit() {
    onSubmit('#f', async (d) => { const r = await api('/keys', 'POST', d); $('#secret').innerHTML = `<p class="warn card">Скопируйте ключ — он больше не будет показан:<br><code>${esc(r.secret)}</code></p>`; });
    document.querySelectorAll('button[data-id]').forEach(b => b.onclick = async () => { if (confirm('Удалить ключ?')) { await api('/keys/' + b.dataset.id, 'DELETE'); route(); } });
  },
  async billing() {
    const b = await api('/balance');
    return `<h1>Баланс</h1><div class="grid"><div class="card"><div class="mut">Доступно</div><div class="big">${b.balance}</div></div><div class="card"><div class="mut">Заработок владельца ноды</div><div class="big">${b.earnings}</div></div></div>
    <div class="card"><h3>Пополнение</h3><p class="mut">Демо-режим: вместо XMR-депозита баланс пополняется мгновенно.</p><form id="t"><input name="amount" type="number" min="1" value="1000"><button>Пополнить</button></form></div>
    <div class="card"><h3>Выплата заработка</h3><p class="mut">Заглушка: заработок обнуляется, реальный перевод не выполняется.</p><button id="p" class="sec">Вывести ${b.earnings}</button></div>`;
  },
  billingInit() {
    onSubmit('#t', async (d) => { await api('/topup', 'POST', d); toast('Баланс пополнен'); route(); });
    $('#p').onclick = async () => { const r = await api('/payout', 'POST'); toast('Выплачено: ' + r.paid_out + ' (заглушка)'); route(); };
  },
  async analytics() {
    const u = await api('/usage'), models = [...new Set(u.map(x => x.model))];
    const draw = (f) => {
      const rows = u.filter(x => !f || x.model === f), days = {};
      rows.forEach(x => { const d = new Date(x.at).toISOString().slice(0, 10); (days[d] ??= { r: 0, t: 0, c: 0 }); days[d].r++; days[d].t += x.in + x.out; days[d].c += x.cost; });
      const ks = Object.keys(days).sort(), mx = Math.max(1, ...ks.map(k => days[k].r));
      return `<div class="grid"><div class="card"><div class="mut">Запросов</div><div class="big">${rows.length}</div></div><div class="card"><div class="mut">Токенов</div><div class="big">${rows.reduce((a, x) => a + x.in + x.out, 0)}</div></div><div class="card"><div class="mut">Потрачено</div><div class="big">${rows.reduce((a, x) => a + x.cost, 0)}</div></div></div>
      <div class="card"><h3>Запросы по дням (UTC)</h3><div class="bars" aria-hidden="true">${ks.map(k => `<div style="height:${days[k].r / mx * 100}%" title="${k}: ${days[k].r}"></div>`).join('')}</div>
      <table><tr><th>День</th><th>Запросов</th><th>Токенов</th><th>Расход</th></tr>${ks.map(k => `<tr><td>${k}</td><td>${days[k].r}</td><td>${days[k].t}</td><td>${days[k].c}</td></tr>`).join('')}</table></div>
      <div class="card"><h3>Последние запросы</h3><table><tr><th>Время</th><th>Модель</th><th>Вход</th><th>Выход</th><th>Цена</th><th>Статус</th></tr>${rows.slice(0, 20).map(x => `<tr><td>${date(x.at)}</td><td>${esc(x.model)}</td><td>${x.in}</td><td>${x.out}</td><td>${x.cost}</td><td class="${x.ok ? 'ok' : 'bad'}">${x.ok ? 'ok' : 'ошибка'}</td></tr>`).join('') || '<tr><td colspan=6 class="mut">Пока пусто</td></tr>'}</table></div>`;
    };
    pages._draw = draw;
    return `<h1>Аналитика</h1><select id="m"><option value="">Все модели</option>${models.map(m => `<option>${esc(m)}</option>`).join('')}</select><div id="out">${draw('')}</div>`;
  },
  analyticsInit() { $('#m').onchange = (e) => $('#out').innerHTML = pages._draw(e.target.value); },
  docs: () => `<h1>Документация</h1><ul><li><a href="#/docs/api">API</a></li><li><a href="#/docs/node-setup">Запуск ноды</a></li><li><a href="#/docs/billing">Биллинг</a></li><li><a href="#/docs/privacy">Приватность</a></li><li><a href="#/docs/troubleshooting">Проблемы</a></li></ul>`,
  'docs/api': () => `<h1>API</h1><p>Base URL: <code>${location.origin}/v1</code>. Авторизация: <code>Authorization: Bearer cvk_…</code>. Модели: <code>GET /v1/models</code>.</p>
   <pre>curl ${location.origin}/v1/chat/completions \\
  -H "Authorization: Bearer cvk_..." \\
  -d '{"model":"tiny-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}'</pre>
   <p>Поддерживаются: <code>model, messages, max_tokens, stream</code>. Tools/functions → 400 <code>unsupported_parameter</code>. Ошибки: 401 ключ, 404 модель, 429 <code>insufficient_funds</code>, 503 <code>no_capacity</code>/<code>timeout</code>.</p>`,
  'docs/node-setup': () => `<h1>Запуск ноды</h1><ol><li>Нужен Node 18+ и файл <code>node.js</code> из репозитория.</li><li>Создайте ноду на странице «Ноды», сохраните секрет.</li><li>Запустите: <pre>SERVER=${location.origin} NODE_SECRET=nsk_... node node.js</pre></li><li>Для реальной модели добавьте <code>BACKEND=http://localhost:11434/v1 BACKEND_MODEL=llama3</code> (ollama / llama-server).</li><li>Остановка: Ctrl+C.</li></ol>`,
  'docs/billing': () => `<h1>Биллинг</h1><p>Цена: 100 единиц за 1000 токенов (оценка: символы ÷ 4). Перед запросом резервируется максимум, после ответа списывается фактическая сумма, остаток возвращается. Ошибка/отмена — полный возврат. Владелец ноды получает 80%. Пополнение и выплата — демо-заглушки, XMR нет.</p>`,
  'docs/privacy': () => `<h1>Приватность</h1><p>Ноды получают тексты запросов открытым текстом и могут их сохранить или подделать ответ. Сервер хранит метаданные использования (модель, число токенов, цена), но не тексты. Ключи, сессии и секреты нод хранятся в виде хешей. Пароли — scrypt.</p>`,
  'docs/troubleshooting': () => `<h1>Проблемы</h1><table><tr><th>Ошибка</th><th>Причина</th></tr><tr><td>404 model_not_found</td><td>Модели нет в списке <code>/v1/models</code></td></tr><tr><td>429 insufficient_funds</td><td>Пополните баланс</td></tr><tr><td>503 no_capacity</td><td>Нет нод онлайн (последний контакт &gt; 30 с)</td></tr><tr><td>503 timeout</td><td>Нода не ответила за 60 с</td></tr><tr><td>401</td><td>Неверный или удалённый ключ</td></tr></table>`,
};
const priv = (r) => r.startsWith('dashboard');
async function route() {
  let r = location.hash.replace(/^#\/?/, '');
  if (ses && !me) me = (await api('/session').catch(() => ({}))).username || null;
  if (priv(r) && !me) { location.hash = '#/login'; return; }
  if (me && (r === 'login' || r === 'register')) { location.hash = '#/dashboard'; return; }
  nav(r);
  const key = r.replace(/^dashboard\/?/, '') || (priv(r) ? 'dashboard' : r);
  const page = pages[priv(r) ? key : r];
  if (!page) { app.innerHTML = '<h1>404</h1>'; return; }
  try { app.innerHTML = await page(); pages[(priv(r) ? key : r) + 'Init']?.(); } catch (e) { app.innerHTML = `<div class="card bad">Ошибка: ${esc(e.message)}</div>`; }
}
addEventListener('hashchange', route); route();
