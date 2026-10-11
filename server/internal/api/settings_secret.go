package api

// Экран одного секрета: `settings:secrets@<имя>~вкладка`. Шапку (путь назад,
// вкладки, закреплённая строка) рисует общий entityFrame, содержимое — здесь:
//
//	Обзор       — что за секрет: вид, заметка, когда заведён и менялся, публичная
//	              часть ключа SSH с «Скопировать»; «Заменить значение» — поле, а не
//	              вопрос; «Опасное» внизу
//	Кому выдан  — матрица «секрет × потребитель»: модули, которым секрет выдан
//	              или которые его просят, — с выдачей и отзывом нажатием; ниже те,
//	              кто берёт секрет сам: серверы, серверы MCP, ящики, провайдеры
//	Журнал      — обращения к этому секрету и правки записи: чипы отбора, двадцать
//	              строк, «Показать ещё»
//
// Значение секрета не попадает ни в разметку, ни в описание экрана ни в каком
// виде: ни в `Value`, ни в `$copy`, ни в подсказке, ни в ошибке. Поле «Заменить
// значение» приходит пустым — «задан» говорит подсказка. Публичную часть ключа
// SSH показывать можно, приватную — никогда. Открытие экрана в журнал «чтение»
// не пишет: экран спрашивает запись, а не значение.
//
// Решений экран не принимает: хранилище, выдача и журнал — как были, он зовёт
// прежние действия (`grant_secret`, `revoke_secret`, `delete_secret`) и одно
// новое по имени, но старое по смыслу, — `save_secret_value`, которое сохраняет
// полоса вместо вопроса `replace_secret`.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// secretTabs — вкладки секрета. Счётчик у вкладки говорит, что там есть решать,
// до того как человек туда зайдёт.
func (s *Server) secretTabs(sec *store.Secret, consumers []engine.SecretConsumer, asks []*engine.Module) []innerTab {
	st := s.eng.Store()
	tabs := []innerTab{
		{ID: secretsTabList, Name: "Обзор"},
		{ID: secretTabGrants, Name: "Кому выдан"},
		{ID: secretsTabLog, Name: "Журнал"},
	}
	if len(consumers) > 0 {
		tabs[1].Count = strconv.Itoa(len(consumers))
	}
	if len(asks) > 0 {
		tabs[1].Count, tabs[1].Level = strconv.Itoa(len(asks))+" просят", LevelWarn
	}
	if n, _ := st.SecretDenials(sec.Name); n > 0 {
		tabs[2].Count, tabs[2].Level = plural(n, "отказ", "отказа", "отказов"), LevelWarn
	}
	return tabs
}

// secretCaption — одна строка фактов закреплённой шапки: вид, кто завёл, кто
// пользуется.
func secretCaption(sec *store.Secret, consumers []engine.SecretConsumer) string {
	parts := []string{engine.SecretKindTitle(sec.Kind)}
	if sec.Service() {
		parts = append(parts, "служебный: заводит Сёма")
	} else {
		parts = append(parts, "заведён вами "+whenText(sec.Created))
	}
	parts = append(parts, secretUsage(consumers))
	return strings.Join(parts, " · ")
}

func (s *Server) buildSecret(doc *Document, a secretsAddr, filter string, limit int) error {
	st := s.eng.Store()
	doc.Layout = "read"
	sec, ok := st.Secret(a.Name)
	if !ok {
		return s.buildMissingSecret(doc, a, filter, limit)
	}
	consumers := s.eng.SecretConsumerIndex()[sec.Name]
	asks := s.eng.SecretAskers(sec.Name)
	add := func(c Component) { doc.Components = append(doc.Components, c) }

	switch a.Tab {
	case secretTabGrants:
		s.secretGrants(add, sec, consumers, asks)
	case secretsTabLog:
		if err := s.secretsJournal(doc, sec.Name, filter, limit); err != nil {
			return err
		}
	default:
		a.Tab = secretsTabList
		s.secretOverview(add, sec, consumers)
	}

	letter, color := secretTile(sec)
	e := entity{Section: secretsScreen, Back: "Ключи и секреты", ID: sec.Name, Name: sec.Name,
		Avatar: letter, AvatarColor: color, Caption: secretCaption(sec, consumers),
		Tab: a.Tab, Tabs: s.secretTabs(sec, consumers, asks), Switch: "Секрет"}
	e.Badges = []Badge{secretKindPill(sec)}
	if sec.Service() {
		e.Badges = append(e.Badges, Badge{Text: "служебный"})
	}
	if n, _ := st.SecretDenials(sec.Name); n > 0 {
		e.Level = LevelAlert
	}
	// одна главная кнопка: выдать тому, кто просит; у ключа SSH — публичная часть
	switch {
	case len(asks) > 0:
		grant := Action{Action: "grant_secret", Target: asks[0].Name + "~" + sec.Name, Label: "Выдать «" + asks[0].Title + "»", Style: "primary"}
		e.Primary = &grant
	case sec.Kind == store.SecretSSH && sec.Public != "" && !sec.Service():
		copyKey := Action{Action: "$copy", Target: sec.Public, Label: "Скопировать публичный ключ"}
		e.Primary = &copyKey
	}
	e.Menu = []Action{
		{Action: "$goto", Target: entityAddr(secretsScreen, sec.Name, secretTabGrants), Label: "Кому выдан"},
		{Action: "$goto", Target: entityAddr(secretsScreen, sec.Name, secretsTabLog), Label: "Журнал"},
	}
	// соседи по группе: ваши переключаются между вашими, служебные между служебными
	if all, err := st.VaultSecrets(); err == nil {
		for _, other := range all {
			if other.Service() == sec.Service() {
				e.Others = append(e.Others, other.Name)
			}
		}
		sort.Strings(e.Others)
	}
	entityFrame(doc, e)
	return nil
}

// buildMissingSecret — секрета нет: его удалили, пока экран был открыт, или к имени
// только обращались. Не ошибка, а объяснение, а журнал — если к имени
// обращались — остаётся жить: ссылка «Журнал» у строки «Просили, а такого нет»
// ведёт сюда.
func (s *Server) buildMissingSecret(doc *Document, a secretsAddr, filter string, limit int) error {
	st := s.eng.Store()
	doc.Heading = "Ключи и секреты"
	doc.Components = append(doc.Components,
		Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: secretsScreen, Label: "‹ Ключи и секреты"}}})
	empty := emptyState("Секрета «"+a.Name+"» нет",
		"Его удалили или не заводили. Список — по кнопке «‹ Ключи и секреты» выше; журнал обращений к этому имени, если они были, — ниже", "key")
	empty.Actions = []Action{{Action: "$goto", Target: entityAddr(secretsScreen, secretsNew, secretsNewName+a.Name), Label: "Завести", Style: "primary"}}
	doc.Components = append(doc.Components, empty)
	if st.CountSecretLogFiltered(a.Name, store.SecretLogAll) > 0 {
		return s.secretsJournal(doc, a.Name, filter, limit)
	}
	return nil
}

// secretOverview — «Обзор»: что за секрет. Свойства — строки, замена значения —
// поле, опасное — внизу.
func (s *Server) secretOverview(add func(Component), sec *store.Secret, consumers []engine.SecretConsumer) {
	st := s.eng.Store()

	// отказы — вверху: модуль просил, и ему сказали «нет»
	if n, last := st.SecretDenials(sec.Name); n > 0 && last != nil {
		add(Component{Type: "section", ID: "secret-alerts", Children: []Component{{
			Type: "row", Layout: "wide", ID: "secret-denied", Level: LevelAlert, Dot: levelDot(LevelAlert),
			Subtitle: "Отказано " + plural(n, "раз", "раза", "раз"),
			Caption:  "Последний: " + last.Who + " просил " + whenText(last.At) + " · " + last.Note,
			Actions:  []Action{{Action: "$goto", Target: entityAddr(secretsScreen, sec.Name, secretsTabLog), Label: "Журнал"}}}}})
	}

	// ---- свойства ----
	rows := []Component{}
	kind := engine.SecretKindTitle(sec.Kind)
	if sec.Service() {
		kind += " · служебный: его завёл сам Сёма и сам обновляет"
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-kind", Subtitle: "Вид", Caption: kind})
	if sec.Note != "" {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-note", Subtitle: "Заметка", Caption: sec.Note})
	}
	created := "неизвестно когда"
	if sec.Created != "" {
		created = whenText(sec.Created)
	}
	who := "вами"
	if sec.Service() {
		who = "самим Сёмой"
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-created", Subtitle: "Заведён", Caption: created + ", " + who})
	changed := "ни разу с тех пор, как заведён"
	if u := st.LastSecretChange(sec.Name, engine.SecretChangeReplaced); u != nil {
		changed = whenText(u.At)
	}
	rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-changed", Subtitle: "Значение менялось", Caption: changed})
	if u := st.LastSecretUse(sec.Name); u != nil {
		verdict := "выдан"
		if !u.Granted {
			verdict = "отказано"
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-last-use", Subtitle: "Брали в последний раз",
			Caption: whoTook(u.Who) + " · " + whenText(u.At) + " · " + verdict})
	} else {
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-last-use", Subtitle: "Брали в последний раз", Caption: "ни разу"})
	}

	// публичная часть ключа SSH — не секрет: её кладут на сервер. Приватная часть
	// закрыта и не показывается и не копируется никому
	if sec.Kind == store.SecretSSH && !sec.Service() {
		if sec.Public != "" {
			install := "echo \"" + sec.Public + "\" >> ~/.ssh/authorized_keys"
			rows = append(rows,
				Component{Type: "row", Layout: "wide", ID: "secret-public", Subtitle: "Публичная часть",
					Caption: sec.Public,
					Actions: []Action{{Action: "$copy", Target: sec.Public, Label: "Скопировать"}}},
				Component{Type: "row", Layout: "wide", ID: "secret-install", Subtitle: "Команда для сервера",
					Caption: "Выполните на сервере, чтобы он принял этот ключ: " + install,
					Actions: []Action{{Action: "$copy", Target: install, Label: "Скопировать команду"}}})
		} else {
			rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-public", Subtitle: "Публичная часть",
				Caption: "неизвестна: ключ импортирован не в формате PKCS#8 ed25519"})
		}
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "secret-private", Subtitle: "Приватная часть",
			Caption: "Закрыта: Сёма не показывает её и не копирует никому. Ею пользуется только ядро, когда заходит на сервер"})
	}
	add(Component{Type: "section", ID: "secret-facts", Children: rows})

	// ---- замена значения ----
	switch {
	case sec.Service():
		var owner []Component
		for _, c := range consumers {
			if target, label := secretConsumerScreen(c); target != "" {
				owner = append(owner, Component{Type: "row", Layout: "wide", ID: "secret-owner-" + c.Kind + "-" + c.Name,
					Subtitle: c.Title, Caption: "Значение заводит и меняет он: здесь не меняется и не удаляется",
					Actions: []Action{{Action: "$goto", Target: target, Label: label}}})
			}
		}
		if len(owner) == 0 {
			owner = append(owner, Component{Type: "row", Layout: "wide", ID: "secret-owner",
				Subtitle: "У секрета нет другого владельца",
				Caption:  "Сёма завёл его для своих дел и обновляет сам; здесь значение не меняется и не удаляется"})
		}
		add(Component{Type: "section", ID: "secret-owners", Title: "Где он живёт", Children: owner})
	case sec.Kind == store.SecretSSH:
		add(Component{Type: "section", ID: "secret-replace-no", Children: []Component{{
			Type: "row", Layout: "wide", ID: "secret-replace-ssh", Subtitle: "Заменить значение",
			Caption: "Ключ SSH не заменяется значением: удалите его и сгенерируйте новый, а публичную часть положите на сервер заново"}}})
	default:
		// пустое поле — «не трогали»: значение сюда не приходит, пока человек не
		// введёт новое. Оно же не показывается ни в разметке, ни в полосе
		// сохранения, ни в ошибке
		add(Component{Type: "form", ID: "secret-value", Style: formRows,
			Fields: []Field{{Type: "secure", Name: "value", Label: "Заменить значение",
				Hint: "задан · значение не показывается: введите новое, чтобы заменить; пусто — оставить прежнее"}},
			Submit: &Action{Action: "save_secret_value", Target: sec.Name, Label: "Сохранить"}})
	}

	// ---- опасное: внизу своей карточкой, с вопросом. Служебному удаления нет ----
	if !sec.Service() {
		del := s.deleteSecretAction(sec, consumers)
		del.Label = "Удалить…"
		caption := "Значение пропадёт безвозвратно, а модули, которым секрет выдан, его потеряют"
		if len(consumers) > 0 {
			var who []string
			for _, c := range consumers {
				who = append(who, c.Title)
			}
			caption += ". Без ключа останутся: " + strings.Join(who, ", ")
		}
		add(Component{Type: "section", ID: "secret-danger", Title: "Опасное", Style: "danger", Children: []Component{{
			Type: "row", Layout: "wide", ID: "danger-delete", Subtitle: "Удалить секрет", Caption: caption,
			Actions: []Action{del}}}})
	}
}

// secretGrants — «Кому выдан»: матрица «секрет × потребитель». Выдаёт и
// отзывает человек, и только модулям: остальные потребители берут секрет сами и
// по своему устройству. Ключ SSH и токены входа модулю не выдаются никогда.
func (s *Server) secretGrants(add func(Component), sec *store.Secret, consumers []engine.SecretConsumer, asks []*engine.Module) {
	switch sec.Kind {
	case store.SecretSSH:
		add(Component{Type: "section", ID: "grants-no", Title: "Модулям", Children: []Component{{
			Type: "row", Layout: "wide", ID: "grants-no-ssh", Subtitle: "Не выдаётся",
			Caption: "Приватная часть ключа SSH не выдаётся ни модулю, ни модели: ею пользуется только ядро, когда заходит на сервер"}}})
	case store.SecretOAuth:
		add(Component{Type: "section", ID: "grants-no", Title: "Модулям", Children: []Component{{
			Type: "row", Layout: "wide", ID: "grants-no-oauth", Subtitle: "Не выдаётся",
			Caption: "Токены входа модулю не выдаются: модуль ходит на сервер MCP через sema.mcp.call, а токен держит ядро"}}})
	default:
		s.grantsMatrix(add, sec, asks)
	}

	// те, кто берёт секрет сам: показываем и ведём к ним
	var core []Component
	for _, c := range consumers {
		if c.Grantable() {
			continue
		}
		row := Component{Type: "row", Layout: "wide", ID: "grants-core-" + c.Kind + "-" + c.Name, Subtitle: c.Title,
			Caption: "берёт сам, когда нужно; выдавать и отзывать не требуется"}
		if target, label := secretConsumerScreen(c); target != "" {
			row.Actions = []Action{{Action: "$goto", Target: target, Label: label}}
		}
		core = append(core, row)
	}
	if len(core) > 0 {
		add(Component{Type: "section", ID: "grants-core", Title: "Берёт само ядро", Children: core})
	}
}

// grantsMatrix — модули, которым секрет выдан или которые его просят: одна
// колонка — этот секрет, клетка переключается нажатием. Модуль, который секрет
// не просит и не получал, в матрицу не попадает: выдавать ему нечего, он про
// секрет не знает.
func (s *Server) grantsMatrix(add func(Component), sec *store.Secret, asks []*engine.Module) {
	holders := s.eng.SecretUsers(sec.Name)
	byName := map[string]*engine.Module{}
	for _, m := range holders {
		byName[m.Name] = m
	}
	for _, m := range asks {
		byName[m.Name] = m
	}
	modules := make([]*engine.Module, 0, len(byName))
	for _, m := range byName {
		modules = append(modules, m)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Title < modules[j].Title })

	if len(modules) == 0 {
		add(Component{Type: "section", ID: "grants-modules", Title: "Модулям", Children: []Component{emptyState(
			"Ни один модуль не просит этот секрет",
			"Модуль называет секрет в манифесте (`secrets`) — тогда он появится здесь, и вы выдадите его одним нажатием. Права «все секреты» нет: только поимённо", "key")}})
		return
	}
	m := Component{Type: "matrix", ID: "grants-matrix", Title: "Выдача модулям · кто какой секрет получит",
		Columns: []string{sec.Name}}
	for _, mod := range modules {
		cell := Cell{}
		if mod.HasGrant(sec.Name) {
			cell.State = "on"
			cell.Title = "выдан: «" + mod.Title + "» получает «" + sec.Name + "» по имени"
			cell.Action = Action{Action: "revoke_secret", Target: mod.Name + "~" + sec.Name, Label: "Отозвать"}
		} else {
			cell.State = "off"
			cell.Title = "не выдан: «" + mod.Title + "» просит его, а получить не может"
			cell.Action = Action{Action: "grant_secret", Target: mod.Name + "~" + sec.Name, Label: "Выдать"}
		}
		caption := "модуль"
		if !mod.HasGrant(sec.Name) {
			caption = "просит в манифесте"
		}
		m.Children = append(m.Children, Component{Type: "row", ID: "grant-" + mod.Name, Subtitle: mod.Title,
			Caption: caption, Cells: []Cell{cell}})
	}
	m.Caption = "Нажатие переключает: галочка — выдан, пусто — не выдан. Модуль получает секрет по имени и только пока выдан; каждое обращение видно в журнале."
	add(m)
}

// ---- действия секрета ----

// saveSecretValue — «Заменить значение» полем: пустое — «не трогали», иначе
// прежняя замена значения. Значение дальше не живёт ни в ответе, ни в ошибке.
func (s *Server) saveSecretValue(req actionRequest) (string, error) {
	name := strings.TrimSpace(req.Target)
	if name == "" {
		return "", fmt.Errorf("нужно имя секрета")
	}
	value := req.Params["value"]
	if strings.TrimSpace(value) == "" {
		return entityAddr(secretsScreen, name, ""), nil
	}
	if err := s.eng.ReplaceSecret(name, value); err != nil {
		return "", err
	}
	return entityAddr(secretsScreen, name, ""), nil
}
