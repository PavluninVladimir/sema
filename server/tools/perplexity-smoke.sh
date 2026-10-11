#!/bin/sh
# Дымовая проба Agent API: печатает только код ответа и форму ответа.
# Ключ берётся из окружения и никуда не пишется.
if [ -z "$PERPLEXITY_API_KEY" ]; then
  echo "PERPLEXITY_API_KEY не задан"; exit 2
fi
code=$(curl -s -o /tmp/ppx_out.json -w '%{http_code}' https://api.perplexity.ai/v1/agent \
  -H "Authorization: Bearer $PERPLEXITY_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"openai/gpt-5.6-luna","input":"Какой сегодня курс доллара к рублю?","tools":[{"type":"web_search"}]}')
echo "HTTP $code"
python3 - <<'PY'
import json
try:
    d=json.load(open('/tmp/ppx_out.json'))
except Exception as e:
    print('ответ не разобран:', e); raise SystemExit
print('поля ответа:', sorted(d.keys()))
print('статус:', d.get('status'))
print('пунктов output:', len(d.get('output') or []), [i.get('type') for i in (d.get('output') or [])])
u=d.get('usage') or {}
print('токены:', u.get('input_tokens'), '/', u.get('output_tokens'))
PY
