package engine

// Обратная сторона провода: не Сёма спрашивает агента, а агент Сёму.
// Раньше память жила за сетевой ручкой с токеном, и мостик рвался при
// каждом обновлении Сёмы, потому что сессия клиента жила в памяти
// контейнера. Житель машины стоит под launchd, переживает выкаты и уже
// держит провод — значит дверью в память должен быть он.
//
// Что можно спрашивать, решает человек: способность «память» появляется
// выключенной, как «код». Что именно будет видно — решают области,
// выданные паре «агент плюс рабочая папка» на экране «Память»: агент
// один, а проектов много, и разделение по папкам остаётся прежним.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// CanMemory — способность спрашивать память. Имя то же, что человек видит
// на экране «Агенты».
const CanMemory = "память"

// agentAskTimeout — сколько Сёма отвечает агенту. Память ходит к модели за
// эмбеддингом, поэтому срок не секундный, но и не бесконечный: житель
// машины ждёт ответа, а за ним ждёт человек.
const agentAskTimeout = 2 * time.Minute

// answerAgent — ответ на запрос агента. Ошибка возвращается кадром, а не
// разрывом: соединение общее, и ронять его из-за одного отказа нельзя.
func (l *link) answerAgent(parent context.Context, e *Engine, name string, id int, method string, params json.RawMessage) {
	ctx, cancel := context.WithTimeout(parent, agentAskTimeout)
	defer cancel()
	res, err := e.handleAgentAsk(ctx, name, method, params)
	if ctx.Err() != nil {
		return
	}
	e.store.LogAgent(store.AgentUse{Agent: name, What: "←" + method, OK: err == nil,
		Detail: errText(err)})
	frame := map[string]any{"jsonrpc": "2.0", "id": id}
	if err != nil {
		frame["error"] = map[string]any{"code": -32000, "message": err.Error()}
	} else {
		frame["result"] = res
	}
	body, merr := json.Marshal(frame)
	if merr != nil {
		return
	}
	_ = l.write(body)
}

// handleAgentAsk — что Сёма умеет отвечать агенту. Список закрытый:
// память и ничего больше. Неизвестное имя — отказ, а не молчание.
func (e *Engine) handleAgentAsk(ctx context.Context, agent, method string, params json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	can, _, ok := strings.Cut(method, ".")
	if !ok {
		return nil, fmt.Errorf("непонятный вызов %q", method)
	}
	if can == CanGateway {
		// шлюз просит открыть поток: соединение под него открывает
		// Сёма, потому что порт наружу есть только у него
		if !e.AgentAllowed(agent, CanGateway) {
			return nil, fmt.Errorf("способность «%s» агенту «%s» не разрешена — включите её на экране «Агенты»", CanGateway, agent)
		}
		// способность разрешена, но роль важнее: тумблер человек мог
		// оставить у агента, которому потом сменили роль новым приглашением
		if a, ok := e.store.Agent(agent); !ok || AgentRole(a) != RoleGateway {
			return nil, fmt.Errorf("агент «%s» не шлюз: поток открывать не для кого", agent)
		}
		return e.gatewayAsk(ctx, agent, strings.TrimPrefix(method, CanGateway+"."), params)
	}
	if can == CanNotify {
		// уведомления: житель машины добирает пропущенное и спрашивает счётчик
		if !e.AgentAllowed(agent, CanNotify) {
			return nil, fmt.Errorf("способность «%s» агенту «%s» не разрешена — включите её на экране «Агенты»", CanNotify, agent)
		}
		return e.notifyAsk(ctx, strings.TrimPrefix(method, CanNotify+"."), params)
	}
	if can != CanMemory {
		return nil, fmt.Errorf("Сёма не отвечает на «%s»: агенту доступны память и уведомления", method)
	}
	if !e.AgentAllowed(agent, CanMemory) {
		return nil, fmt.Errorf("способность «%s» агенту «%s» не разрешена — включите её на экране «Агенты»", CanMemory, agent)
	}
	return e.memoryAsk(ctx, memoryWho(agent, params), strings.TrimPrefix(method, CanMemory+"."), params)
}
