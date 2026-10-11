package api

// Содержимое экрана настроек по его (уже канонизированному) адресу — то, что
// стоит между колонкой разделов и нижней полосой. Раньше этот выбор жил внутри
// `buildScreenForPhone`; индекс настроек (`settings_index.go`) собирает экраны
// без оболочки — без меню, колонки и поиска по разделам, — и ему нужен ровно
// этот выбор, поэтому он вынесен.

import (
	"strings"
)

// buildSettingsBody — собрать содержимое экрана настроек. Возвращает поиск,
// который остаётся общему поиску по разделам: у экрана со своим списком
// (чаты Telegram, модели, правила сервера MCP) верхнее поле ищет по списку, и
// набранное в разделы уже не уходит.
func (s *Server) buildSettingsBody(doc *Document, screenName, filter, search string, limit int, phone bool) (string, error) {
	var err error
	switch {
	case screenName == accessScreen || strings.HasPrefix(screenName, accessScreen+"~"):
		_, tab := splitSubtab(screenName)
		doc.ActiveTab = accessScreen
		err = s.buildAccess(doc, tab, filter, limit)
	case screenName == storagesScreen || strings.HasPrefix(screenName, storagesScreen+"~") || strings.HasPrefix(screenName, storagesScreen+"@"):
		// «settings:storages~sources», «settings:storages@yandex», «settings:storages@new»:
		// вкладка раздела, экран хранилища или «Добавить хранилище»
		doc.ActiveTab = storagesScreen
		err = s.buildStorages(doc, screenName)
	case screenName == notifySettingsScreen || strings.HasPrefix(screenName, notifySettingsScreen+"~") || strings.HasPrefix(screenName, notifySettingsScreen+"@"):
		// «settings:notify~rules», «settings:notify@new»: вкладка раздела или «Добавить канал»
		doc.ActiveTab = notifySettingsScreen
		err = s.buildNotifySettings(doc, screenName)
	case screenName == agentsScreen || strings.HasPrefix(screenName, agentsScreen+"@") || strings.HasPrefix(screenName, agentsScreen+"~"):
		// «settings:agents@мак~caps», «settings:agents~gateway»,
		// «settings:agents@new»: вкладка раздела, экран агента или приглашение
		doc.ActiveTab = agentsScreen
		err = s.buildAgents(doc, screenName, filter, limit)
	case screenName == maxScreen:
		doc.ActiveTab = maxScreen
		err = s.buildMAX(doc)
	case screenName == tgScreen || strings.HasPrefix(screenName, tgScreen+"@") || strings.HasPrefix(screenName, tgScreen+"~"):
		// «settings:telegram@79990000000~answer»: учётка и внутренняя вкладка
		addr, tab := splitSubtab(screenName)
		учётка := ""
		if _, a, ok := strings.Cut(addr, "@"); ok {
			учётка = a
		}
		doc.ActiveTab = tgScreen
		err = s.buildTelegram(doc, учётка, tab, filter, search, limit)
		// на «Собеседниках» верхнее поле — поиск чатов, а не общий поиск по
		// разделам: набранное ищет чаты на сервере
		if doc.ownSearch {
			search = ""
		}
	case screenName == mcpScreen || strings.HasPrefix(screenName, mcpScreen+"@") || strings.HasPrefix(screenName, mcpScreen+"~"):
		// «settings:integrations@planner~rules»: сервер и его вкладка.
		// У самого раздела вкладок нет, а старые закладки («~trackers»)
		// открывают его, а не ошибку (canonMCPAddr)
		doc.ActiveTab = mcpScreen
		err = s.buildMCP(doc, screenName, filter, search, limit)
		// на вкладке «Правила» поиск сверху ищет инструменты, а не
		// настройки: поле одно на экран, и набранное нужно этому списку
		if mcpOwnSearch(screenName) {
			search = ""
		}
	case screenName == learningScreen:
		doc.ActiveTab = learningScreen
		err = s.buildLearning(doc)
	case screenName == searchScreen:
		doc.ActiveTab = searchScreen
		err = s.buildSearchSettings(doc)
	case screenName == changesScreen:
		doc.ActiveTab = changesScreen
		err = s.buildSettingsChanges(doc, filter, limit)
	case screenName == sentScreen || strings.HasPrefix(screenName, sentScreen+"~"):
		// «settings:sent» — правила и хранение; «settings:sent~log» — лента
		// отправок; «settings:sent~<id>» — одна отправка целиком. Прежние
		// адреса («sent», «sent~log», «sent~12») приводит canonSentAddr
		doc.ActiveTab = sentScreen
		err = s.buildSentScreen(doc, screenName, filter, limit)
	case screenName == watchScreen || strings.HasPrefix(screenName, watchScreen+"~"):
		// «settings:watch~screen», «settings:watch~chats»: вкладка раздела
		doc.ActiveTab = watchScreen
		err = s.buildWork(doc, screenName)
	case screenName == skillsScreen || strings.HasPrefix(screenName, skillsScreen+"@"):
		// «settings:skills@linux-admin», «settings:skills@new»: экран скилла и «Добавить скилл»
		doc.ActiveTab = skillsScreen
		err = s.buildSkills(doc, screenName)
	case strings.HasPrefix(screenName, mailScreen):
		// «settings:mail~spam», «settings:mail@ящик~folders», «settings:mail@new»:
		// вкладка раздела или экран ящика со своей вкладкой
		doc.ActiveTab = mailScreen
		err = s.buildSettingsMail(doc, screenName, limit)
	case screenName == translateScreen:
		doc.ActiveTab = translateScreen
		err = s.buildTranslate(doc)
	case screenName == toolsScreen || strings.HasPrefix(screenName, toolsScreen+"~"):
		// вкладок у раздела больше нет; хвост прежней вкладки («~codes»)
		// разворачивает «Коды на картинках»
		_, tab := splitSubtab(screenName)
		doc.ActiveTab = toolsScreen
		err = s.buildToolsTab(doc, tab)
	case strings.HasPrefix(screenName, serversScreen):
		doc.ActiveTab = serversScreen
		err = s.buildServers(doc, screenName, filter, limit)
	case strings.HasPrefix(screenName, secretsScreen):
		// «settings:secrets@ключ~grants», «settings:secrets~crypt»,
		// «settings:secrets@new»: раздел с тремя вкладками или экран секрета
		doc.ActiveTab = secretsScreen
		err = s.buildSecrets(doc, screenName, filter, limit)
	case screenName == modelsScreen || strings.HasPrefix(screenName, modelsScreen+"~") || strings.HasPrefix(screenName, modelsScreen+"@"):
		// «settings:models~list», «settings:models@gpt-6-luna»,
		// «settings:models@provider:OpenAI~prices», «settings:models@new»
		doc.ActiveTab = modelsScreen
		err = s.buildModels(doc, screenName, filter, search, limit)
		// у списка моделей верхнее поле — его собственный поиск: набранное
		// ищет модели, а общий поиск по разделам над ними не нужен
		if modelsOwnSearch(screenName) {
			search = ""
		}
	case phone && screenName == settingsRoot:
		// на телефоне колонке места нет: корень — список разделов
		err = s.buildSettingsRoot(doc, search, limit)
	default:
		// «Настройки» без раздела — обзор: раньше они байт в байт
		// совпадали с «Моделями», и человек не видел, что где. На телефоне
		// до него идут строкой корня, адресом settings:overview
		doc.ActiveTab = "settings"
		err = s.buildSettingsOverview(doc, search, limit)
	}
	return search, err
}
