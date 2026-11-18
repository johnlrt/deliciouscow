<h1>Запуск ноды (cordova-node)</h1>
<ol>
	<li>Соберите <code>cordova-node</code> из папки <code>node/</code>: <code>cargo build --release</code>.</li>
	<li>Авторизуйте ноду: <code>cordova-node login --server http://127.0.0.1:8443 --name my-gpu</code>.</li>
	<li>Введите код на странице <a href="/dashboard/nodes">Ноды</a>. Секрет сохранится в <code>~/.config/cordova-node/config.json</code> с правами <code>0600</code>.</li>
	<li>Запустите: <code>cordova-node run --backend mock --model cordova-tiny-chat</code> (или <code>--backend llama --llama-url http://127.0.0.1:8081</code>).</li>
</ol>
