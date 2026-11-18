#!/bin/bash
# Сквозной тест: сервер + нода + запрос
set -e; cd "$(dirname "$0")"; export DB=/tmp/mc-test.json PORT=8099; rm -f $DB
node server.js & S=$!; sleep 1; A=http://localhost:8099
j() { node -e "console.log(JSON.parse(require('fs').readFileSync(0))$1)"; }
SES=$(curl -s $A/api/register -d '{"username":"bob","password":"password123"}' | j .session)
AH="authorization: Bearer $SES"
curl -s -H "$AH" $A/api/topup -d '{"amount":1000}' >/dev/null
KEY=$(curl -s -H "$AH" -X POST $A/api/keys | j .secret)
NS=$(curl -s -H "$AH" -X POST $A/api/nodes | j .node_secret)
SERVER=$A NODE_SECRET=$NS node node.js & N=$!; sleep 1
curl -s $A/v1/chat/completions -H "authorization: Bearer $KEY" -d '{"model":"tiny-chat","messages":[{"role":"user","content":"hello"}]}'; echo
curl -sN $A/v1/chat/completions -H "authorization: Bearer $KEY" -d '{"model":"tiny-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}'
curl -s -H "$AH" $A/api/balance; echo
kill $S $N
