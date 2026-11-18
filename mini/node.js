// Мини-нода: опрашивает сервер, выполняет задания. По умолчанию — mock; с BACKEND=http://localhost:11434/v1 проксирует в OpenAI-совместимый бэкенд (ollama/llama-server).
// Запуск: SERVER=http://localhost:8080 NODE_SECRET=nsk_... node node.js
const SERVER = process.env.SERVER || 'http://localhost:8080', SECRET = process.env.NODE_SECRET, BACKEND = process.env.BACKEND;
const H = { authorization: 'Bearer ' + SECRET, 'content-type': 'application/json' };
const post = (id, o) => fetch(`${SERVER}/node/chunk/${id}`, { method: 'POST', headers: H, body: JSON.stringify(o) });

async function run(job) {
  if (!BACKEND) {
    const last = job.messages[job.messages.length - 1].content;
    for (const w of ('mock reply to: ' + last).split(' ')) { await post(job.id, { delta: w + ' ' }); await new Promise(r => setTimeout(r, 50)); }
    return post(job.id, { done: true });
  }
  const r = await fetch(BACKEND + '/chat/completions', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ model: process.env.BACKEND_MODEL || job.model, messages: job.messages, max_tokens: job.max_tokens, stream: true }) });
  let buf = '';
  for await (const c of r.body) {
    buf += Buffer.from(c).toString();
    let i; while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i).trim(); buf = buf.slice(i + 1);
      if (!line.startsWith('data:') || line.includes('[DONE]')) continue;
      const d = JSON.parse(line.slice(5)).choices?.[0]?.delta?.content; if (d) await post(job.id, { delta: d });
    }
  }
  await post(job.id, { done: true });
}

(async () => {
  if (!SECRET) throw new Error('set NODE_SECRET');
  for (;;) {
    try {
      const r = await fetch(SERVER + '/node/poll', { headers: H });
      if (r.status === 200) run(await r.json()).catch(console.error);
      else if (r.status !== 204) await new Promise(r => setTimeout(r, 3000));
    } catch (e) { console.error(e.message); await new Promise(r => setTimeout(r, 3000)); }
  }
})();
