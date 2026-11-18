<h1>OpenAI API</h1>
<p>Base URL: <code>/v1</code> · Заголовок: <code>Authorization: Bearer cvk_...</code></p>
<pre>curl -X POST http://localhost:8443/v1/chat/completions \
  -H "Authorization: Bearer cvk_..." \
  -H "Content-Type: application/json" \
  -d '&#123;"model":"cordova-tiny-chat","stream":true,"messages":[&#123;"role":"user","content":"Hello"&#125;]&#125;'</pre>
<p>Поддерживаемые параметры: <code>model</code>, <code>messages</code>, <code>max_tokens</code>, <code>temperature</code>, <code>top_p</code>, <code>stop</code>, <code>seed</code>, <code>stream</code>, <code>stream_options.include_usage</code>. Параметры <code>tools</code> и <code>functions</code> возвращают 400 <code>unsupported_parameter</code>.</p>
