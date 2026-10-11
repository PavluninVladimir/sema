package api

import (
	"context"
	"strings"
)

// Действия с задачами и мастером задачи.
// Часть общего разбора действий: execute перебирает такие группы, пока
// одна из них не возьмёт действие себе; чужое действие — errUnhandled.

func (s *Server) tasksActions(req actionRequest) (string, error) {
	one := func() (int64, error) { return req.one() }
	switch req.Action {
	case "reset_tasks":
		return "", s.eng.ResetSystemTasks()
	case "new_task_form":
		s.mu.Lock()
		s.showNewTask = !s.showNewTask
		s.editTaskID = 0
		s.mu.Unlock()
		return "", nil
	case "edit_task_form":
		id, err := one()
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		if s.editTaskID == id {
			s.editTaskID = 0 // повторное нажатие закрывает окно
		} else {
			s.editTaskID, s.showNewTask = id, false
		}
		s.mu.Unlock()
		return "", nil
	case "close_task_form":
		s.mu.Lock()
		s.showNewTask, s.editTaskID = false, 0
		s.mu.Unlock()
		return "", nil
	case "save_task":
		id, err := one()
		if err != nil {
			return "", err
		}
		if title := strings.TrimSpace(req.Params["what"]); title != "" {
			if err := s.eng.Store().SetTaskTitle(id, title); err != nil {
				return "", err
			}
			if err := s.eng.Store().SetTaskPrompt(id, title); err != nil {
				return "", err
			}
		}
		if when := scheduleFromForm(req.Params); when != "" {
			if err := s.eng.RescheduleTask(id, when); err != nil {
				return "", err
			}
		}
		s.mu.Lock()
		s.editTaskID = 0
		s.mu.Unlock()
		return "", nil
	case "create_task":
		if err := s.eng.CreateTask(req.Params["what"], scheduleFromForm(req.Params)); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.showNewTask, s.editTaskID = false, 0
		s.mu.Unlock()
		return "", nil
	case "run_task":
		id, err := one()
		if err != nil {
			return "", err
		}
		s.eng.RunTaskByID(context.Background(), id)
		return "", nil
	case "rename_task":
		id, err := one()
		if err != nil {
			return "", err
		}
		title := strings.TrimSpace(req.Params["value"])
		if title == "" {
			return "", nil
		}
		return "", s.eng.Store().SetTaskTitle(id, title)
	case "reschedule_task":
		id, err := one()
		if err != nil {
			return "", err
		}
		return "", s.eng.RescheduleTask(id, req.Params["value"])
	case "retask_prompt":
		id, err := one()
		if err != nil {
			return "", err
		}
		return "", s.eng.Store().SetTaskPrompt(id, strings.TrimSpace(req.Params["value"]))
	case "toggle_task":
		id, err := one()
		if err != nil {
			return "", err
		}
		t, err := s.eng.Store().Task(id)
		if err != nil {
			return "", err
		}
		return "", s.eng.Store().SetTaskEnabled(id, !t.Enabled)
	case "delete_task":
		id, err := one()
		if err != nil {
			return "", err
		}
		script, err := s.eng.DeleteTask(id)
		if err != nil {
			return "", err
		}
		if script != "" {
			s.lastCheck = "задача удалена вместе со скриптом «" + script + "»"
		}
		return "", nil
	default:
		return "", errUnhandled
	}
}

// scheduleFromForm — в шаге «Когда» три поля, и раньше сохранялось только
// одно: человек выбирал чип «каждый час» или задачу в «запускать после», а
// изменения молча не происходило.
//
// Порядок: выбранная задача сильнее всего (это другой способ запуска),
// затем чип, затем написанное руками. Чип и текст спорят редко, но если
// спорят — верим тому, по чему человек только что щёлкнул.
func scheduleFromForm(p map[string]string) string {
	after := strings.TrimSpace(p["after"])
	if after != "" && !strings.HasPrefix(after, "—") {
		return "после «" + after + "»"
	}
	if chip := strings.TrimSpace(p["preset"]); chip != "" {
		return chip
	}
	return strings.TrimSpace(p["when"])
}
