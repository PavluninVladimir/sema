package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Действия вкладки «Инструменты», модулей и категорий трат.
// Часть общего разбора действий: execute перебирает такие группы, пока
// одна из них не возьмёт действие себе; чужое действие — errUnhandled.

func (s *Server) toolsActions(req actionRequest) (string, error) {
	switch req.Action {
	case "rotate_key":
		box, err := secret.New(strings.TrimSpace(req.Params["value"]))
		if err != nil || box == nil {
			return "", fmt.Errorf("новый ключ не подошёл: задайте непустую строку")
		}
		if err := s.eng.Store().RotateKey(box); err != nil {
			return "", err
		}
		log.Printf("ключ шифрования сменён — запускайте сервер с новым значением %s",
			s.eng.KeyEnvName())
		// смена ключа живёт на вкладке «Шифрование» «Ключей и секретов»
		return entityAddr(secretsScreen, "", secretsTabCrypt), nil
	case "install_module":
		source := req.Params["source"]
		if url := strings.TrimSpace(req.Params["url"]); url != "" && source == "" {
			body, err := httpGet(url)
			if err != nil {
				return "", fmt.Errorf("не удалось скачать модуль: %w", err)
			}
			source = body
		}
		m, err := engine.ParseModule(source)
		if err != nil {
			return "", err
		}
		// не ставим молча: сначала человек видит, что модуль просит
		s.askGrants(m)
		return "modules", nil
	case "confirm_module":
		s.mu.Lock()
		m := s.pendingModule
		if m != nil {
			s.eng.CloseNotification("grants:"+m.Name, store.NotifyDone, "права выданы")
		}
		grants := s.pendingGrants
		s.pendingModule, s.pendingGrants = nil, nil
		s.mu.Unlock()
		if m == nil {
			return "modules", nil
		}
		m.Granted = []string{}
		for _, name := range m.Secrets {
			if _, exists := s.eng.Store().Secret(name); grants[name] && exists {
				m.Granted = append(m.Granted, name)
			}
		}
		m.GrantedMemory = []string{}
		for _, area := range m.Memory {
			if _, exists := s.eng.Store().MemoryArea(area); grants["memory:"+area] && exists {
				m.GrantedMemory = append(m.GrantedMemory, area)
			}
		}
		m.GrantedHosts = []string{}
		for _, host := range m.SSH {
			if _, exists := s.eng.Store().Host(host); grants["ssh:"+host] && exists {
				m.GrantedHosts = append(m.GrantedHosts, host)
			}
		}
		m.GrantedMCP = []string{}
		for _, srv := range m.MCP {
			if _, exists := s.eng.Store().MCPServer(srv); grants["mcp:"+srv] && exists {
				m.GrantedMCP = append(m.GrantedMCP, srv)
			}
		}
		m.GrantedEvents = []string{}
		for _, kind := range engine.EventKinds() {
			if m.Handler(kind) != "" && grants["event:"+kind] {
				m.GrantedEvents = append(m.GrantedEvents, kind)
			}
		}
		if err := s.eng.InstallModule(m); err != nil {
			return "", err
		}
		if req.Target == "enable" {
			return "modules", s.eng.EnableModule(m.Name, true)
		}
		return "modules", nil
	case "cancel_module":
		s.mu.Lock()
		s.pendingModule, s.pendingGrants = nil, nil
		s.mu.Unlock()
		return "modules", nil
	case "set_module_instructions":
		return "modules", s.eng.SetModuleInstructions(req.Target, req.Params["text"])
	case "accept_rule":
		module, rest := splitTarget(req.Target)
		// новая кнопка шлёт номер сообщения с карточкой, старая — сам текст
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil && id > 0 {
			return "", s.eng.AcceptRule(module, id)
		}
		return "", s.eng.AppendModuleRule(module, rest)
	case "module_action":
		// цель «модуль~функция~аргумент»: аргумент кнопки уходит в params["arg"]
		module, rest := splitTarget(req.Target)
		fn, arg := splitTarget(rest)
		params := req.Params
		if arg != "" {
			if params == nil {
				params = map[string]string{}
			}
			params["arg"] = arg
		}
		return "", s.eng.ModuleAction(context.Background(), module, fn, params)
	case "copy_module":
		return "modules", s.eng.CopyModule(req.Target, req.Params["value"])
	case "grant_module_needs":
		return "modules", s.eng.GrantModuleNeeds(req.Target, true)
	case "reject_module_needs":
		return "modules", s.eng.GrantModuleNeeds(req.Target, false)
	case "toggle_module":
		m, ok := s.eng.Module(req.Target)
		if !ok {
			return "", fmt.Errorf("модуль не установлен")
		}
		// встроенный модуль ставится без вопросов, но события ему никто не
		// выдавал: первое включение идёт через тот же экран прав
		if !m.Enabled && m.GrantedEvents == nil && m.HasHandlers() {
			s.askGrants(m)
			return "modules", nil
		}
		// модуль просит серверы, секреты или память, а выдано ещё ничего —
		// включение идёт через экран прав, иначе он включится бесправным
		if !m.Enabled && ((len(m.SSH) > 0 && m.GrantedHosts == nil) || (len(m.Secrets) > 0 && m.Granted == nil) ||
			(len(m.Memory) > 0 && m.GrantedMemory == nil) || (len(m.MCP) > 0 && m.GrantedMCP == nil)) {
			s.askGrants(m)
			return "modules", nil
		}
		return "modules", s.eng.EnableModule(req.Target, !m.Enabled)
	case "remove_module":
		return "modules", s.eng.RemoveModule(req.Target)
	case "set_module_task_timeout":
		// в форме «Пределы» два поля: задача модуля и скрипт — сохраняется
		// то, что пришло
		if raw, ok := req.Params["task_timeout"]; ok {
			v := 0
			if _, err := fmt.Sscanf(raw, "%d", &v); err != nil || v < 30 {
				return "", fmt.Errorf("нужно число секунд от 30")
			}
			if err := s.eng.Store().SetSetting(engine.ModuleTaskTimeoutKey, fmt.Sprint(v)); err != nil {
				return "", err
			}
		}
		if raw, ok := req.Params["script_timeout"]; ok {
			v := 0
			if _, err := fmt.Sscanf(raw, "%d", &v); err != nil || v < 10 {
				return "", fmt.Errorf("нужно число секунд от 10")
			}
			if err := s.eng.Store().SetSetting("scripts.timeout_s", fmt.Sprint(v)); err != nil {
				return "", err
			}
		}
		return "modules", nil
	case "toggle_chat_scripts":
		return "", s.eng.SetChatScriptsEnabled(!s.eng.ChatScriptsEnabled())
	case "set_codes_limits":
		// «Тонкая настройка» «Инструментов»: время на файл и предел размера
		// картинки одной формой; прежние `save_codes_limit` и `save_codes_pixels`
		// (с вариантами списка «15 с», «40 Мп») работают как работали
		return "", s.saveSettings(req,
			settingParam{"limit_s", "codes.max_s", "время на коды одного файла"},
			settingParam{"max_mp", "codes.max_mp", "предел размера картинки"})
	case "toggle_watch":
		return "", s.eng.SetWatchOn(!s.eng.WatchOn())
	case "toggle_watch_stop":
		return "", s.eng.SetWatchStops(!s.eng.WatchStops())
	case "toggle_watch_disagree":
		return "", s.eng.SetWatchDisagree(!s.eng.WatchDisagree())
	case "toggle_watch_doctor":
		return "", s.eng.SetWatchDoctor(!s.eng.WatchDoctor())
	case "save_watch_limits":
		повторов, _ := strconv.Atoi(req.Params["repeat"])
		окно, _ := strconv.Atoi(req.Params["window"])
		хранить, _ := strconv.Atoi(req.Params["keep"])
		return "", s.eng.SetWatchLimits(повторов, окно, хранить)
	case "recheck_tools":
		engine.ToolsAvailable()
		s.eng.ResetVisionState() // вдруг модель сменили на ту, что видит картинки
		// снимаем «смотрели и не нашли»: инструмент мог появиться после
		// того, как файл приняли, — за этим кнопку и нажимают
		if n, err := s.eng.Store().ClearMarksWithoutText(); err == nil && n > 0 {
			log.Printf("перечитывание: снято отметок «текста не нашлось» — %d", n)
		}
		go func() {
			n, err := s.eng.RescanAttachments(context.Background(), 500)
			if err != nil {
				log.Printf("перечитывание файлов: %v", err)
				return
			}
			log.Printf("перечитано файлов: %d", n)
		}()
		return "", nil
	default:
		return "", errUnhandled
	}
}

// askGrants показывает человеку, что модуль просит, и ждёт подтверждения:
// по умолчанию отмечено то, что есть, лишнее человек снимает.
func (s *Server) askGrants(m *engine.Module) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingModule = m
	s.pendingGrants = map[string]bool{}
	for _, name := range m.Secrets {
		_, exists := s.eng.Store().Secret(name)
		s.pendingGrants[name] = exists
	}
	for _, area := range m.Memory {
		_, exists := s.eng.Store().MemoryArea(area)
		s.pendingGrants["memory:"+area] = exists
	}
	for _, host := range m.SSH {
		_, exists := s.eng.Store().Host(host)
		s.pendingGrants["ssh:"+host] = exists
	}
	// серверы MCP — как и остальное: заведённый отмечен, лишнее человек снимает
	for _, srv := range m.MCP {
		_, exists := s.eng.Store().MCPServer(srv)
		s.pendingGrants["mcp:"+srv] = exists
	}
	for _, kind := range engine.EventKinds() {
		if m.Handler(kind) != "" {
			s.pendingGrants["event:"+kind] = true
		}
	}
	var what []string
	if len(m.Secrets) > 0 {
		what = append(what, "секреты: "+strings.Join(m.Secrets, ", "))
	}
	if len(m.Memory) > 0 {
		what = append(what, "память: "+strings.Join(m.Memory, ", "))
	}
	if len(m.SSH) > 0 {
		what = append(what, "серверы: "+strings.Join(m.SSH, ", "))
	}
	if m.HasHandlers() {
		what = append(what, "события писем и файлов")
	}
	s.eng.NotifyModuleGrants(m, what)
}
