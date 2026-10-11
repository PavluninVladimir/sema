package api

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Действия со скриптами.
// Часть общего разбора действий: execute перебирает такие группы, пока
// одна из них не возьмёт действие себе; чужое действие — errUnhandled.

func (s *Server) scriptsActions(req actionRequest) (string, error) {
	one := func() (int64, error) { return req.one() }
	switch req.Action {
	case "new_script":
		id, err := s.eng.Store().CreateScript("Новый скрипт", scriptSample)
		if err != nil {
			return "", err
		}
		return "script:" + strconv.FormatInt(id, 10), nil
	case "save_script":
		id, err := one()
		if err != nil {
			return "", err
		}
		sc, err := s.eng.Store().Script(id)
		if err != nil {
			return "", err
		}
		return "", s.eng.Store().SaveScript(id, sc.Title, req.Params["code"])
	case "rename_script":
		id, err := one()
		if err != nil {
			return "", err
		}
		sc, err := s.eng.Store().Script(id)
		if err != nil {
			return "", err
		}
		title := strings.TrimSpace(req.Params["value"])
		if title == "" {
			return "", nil
		}
		return "", s.eng.Store().SaveScript(id, title, sc.Code)
	case "delete_script":
		id, err := one()
		if err != nil {
			return "", err
		}
		return "scripts", s.eng.DeleteScript(id)
	case "run_script":
		id, err := one()
		if err != nil {
			return "", err
		}
		res, err := s.eng.RunScriptByID(context.Background(), id)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		if s.scriptRuns == nil {
			s.scriptRuns = map[int64]scriptRun{}
		}
		s.scriptRuns[id] = scriptRun{
			At:  time.Now(),
			Log: res.Log, Value: res.Value,
			ErrText: res.ErrText, ErrLine: res.ErrLine, ErrCode: res.ErrCode,
			Seconds: res.Duration.Seconds(),
		}
		s.mu.Unlock()
		return "", nil
	case "script_to_task":
		id, err := one()
		if err != nil {
			return "", err
		}
		sc, err := s.eng.Store().Script(id)
		if err != nil {
			return "", err
		}
		return "tasks", s.eng.CreateScriptTask(sc.ID, sc.Title, strings.TrimSpace(req.Params["value"]))
	default:
		return "", errUnhandled
	}
}
