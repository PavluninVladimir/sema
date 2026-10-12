package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Действия экранов «Агентов». Сами экраны — settings_agents.go (раздел),
// settings_agent.go (один агент), settings_agent_new.go (приглашение).

// stayOnAgents — после действия остаёмся там, где нажали: «Проверить связь» на
// экране агента не должна уводить в список.
func stayOnAgents(req actionRequest, def string) string {
	if strings.HasPrefix(req.Screen, agentsScreen) {
		return req.Screen
	}
	return def
}

func (s *Server) agentsActions(req actionRequest) (string, error) {
	st := s.eng.Store()
	switch req.Action {
	case "copy_root_fp":
		// отпечаток и так виден, а кнопка «Скопировать» берёт его со страницы:
		// отдельного действия на сервере не нужно, оно осталось ради старых
		// клиентов
		return stayOnAgents(req, agentsScreen), nil
	case "invite_agent":
		name := strings.TrimSpace(req.Params["name"])
		if name == "" {
			name = req.Target
		}
		// «Новое приглашение» из строки агента: роль берём его, а не
		// значение формы — иначе кнопка молча переводила бы жителя в
		// шлюзы тем, что в форме стоит другой вариант
		role := strings.TrimSpace(req.Params["role"])
		if req.Params["name"] == "" && req.Target != "" {
			if a, ok := st.Agent(req.Target); ok {
				role = engine.AgentRole(a)
			}
		}
		code, err := s.eng.NewInvite(name, role)
		if err != nil {
			return "", err
		}
		// код показывается один раз: следующая сборка экрана приглашения его
		// забирает, и дальше он не лежит нигде, кроме самого приглашения
		role, _ = engine.KnownRole(role)
		until := time.Now().Add(time.Duration(s.eng.InviteMinutes()) * time.Minute)
		s.mu.Lock()
		s.freshInvite = &freshInvite{Name: name, Role: role, Code: code, Until: until.Format(time.RFC3339)}
		s.mu.Unlock()
		return entityAddr(agentsScreen, agentsNew, ""), nil
	case "drop_invite":
		// приглашение называют отпечатком, а не кодом; прежний вызов с кодом
		// тоже работает
		if _, err := st.DropInviteRef(req.Target); err != nil {
			return "", err
		}
		return stayOnAgents(req, entityAddr(agentsScreen, agentsNew, "")), nil
	case "revoke_agent":
		return stayOnAgents(req, agentsScreen), s.eng.RevokeAgent(req.Target)
	case "accept_agent_fp":
		return stayOnAgents(req, agentsScreen), s.eng.AcceptAgentFP(req.Target)
	case "forget_agent":
		// после «Забыть» экрана агента больше нет: возвращаемся в список
		return agentsScreen, s.eng.ForgetAgent(req.Target)
	case "allow_agent":
		name, can := splitTarget(req.Target)
		if name == "" || can == "" {
			return "", fmt.Errorf("нужно имя агента и способность")
		}
		on := !s.eng.AgentAllowed(name, can)
		return stayOnAgents(req, agentsScreen), s.eng.AllowAgent(name, can, on)
	case "check_agent":
		out, err := s.eng.CheckAgent(context.Background(), req.Target)
		text := out
		if err != nil {
			text = err.Error()
		}
		s.mu.Lock()
		s.lastAgentCheck = agentCheck{Name: req.Target, Text: text}
		s.mu.Unlock()
		return stayOnAgents(req, agentsScreen), nil
	case "set_agent_listen":
		return stayOnAgents(req, agentsScreen), st.SetSetting("agents.listen", strings.TrimSpace(req.Params["listen"]))
	case "set_agent_off_notify":
		return stayOnAgents(req, agentsScreen), s.saveSettings(req,
			settingParam{"off_notify", engine.AgentOffNotifyKey, "срок до уведомления о пропаже"})
	case "set_gateway":
		if err := st.SetSetting("agents.gateway", strings.TrimSpace(req.Params["gateway"])); err != nil {
			return "", err
		}
		return stayOnAgents(req, agentsScreen), st.SetSetting("agents.gateway_upstream", strings.TrimSpace(req.Params["upstream"]))
	}
	return "", errUnhandled
}
