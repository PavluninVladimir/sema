package api

// «Скиллы» (2 октября 2026): список тонкими строками без текста скиллов, экран
// скилла с текстом и «Опасным», «Добавить скилл» отдельным экраном. Скиллы в промпте
// и самообучение не менялись.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Текст скилла едет только на его экран: на экране списка фразы из текста нет ни в
// описании, ни в разметке, а на экране скилла — есть. И список не растёт вместе с
// текстами: двадцать скиллов по пять килобайт его не утяжеляют.
func TestSkillTextTravelsOnlyToSkillScreen(t *testing.T) {
	s, _ := stnFix(t)
	list := stnText(t, s, skillsScreen)
	if strings.Contains(list, stnSkillPhrase) {
		t.Error("текст скилла в разметке списка: он опять едет в каждой строке")
	}
	one := stnText(t, s, engine.SkillScreen("backups"))
	if !strings.Contains(one, stnSkillPhrase) {
		t.Error("на экране скилла нет его текста")
	}
	// ни одно действие списка и шапки скилла не носит текст: копирование идёт по полю
	for _, screen := range []string{skillsScreen, engine.SkillScreen("backups")} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		var walk func(cs []Component)
		walk = func(cs []Component) {
			for _, c := range cs {
				for _, a := range append(append([]Action{}, c.Actions...), c.Menu...) {
					if len(a.Target) > 200 || strings.Contains(a.Target, stnSkillPhrase) {
						t.Errorf("%s: действие %q несёт текст в цели (%d знаков)", screen, a.Action, len(a.Target))
					}
				}
				walk(c.Children)
			}
		}
		walk(doc.Components)
	}
	// у «Скопировать текст» цель — имя поля, и такое поле на экране есть
	doc, _ := s.buildScreen(engine.SkillScreen("backups"), "", "", 0)
	head := doc.Components[1].Children[0]
	var copyAct *Action
	for i := range head.Menu {
		if head.Menu[i].Action == "$copy_field" {
			copyAct = &head.Menu[i]
		}
	}
	if copyAct == nil || copyAct.Target != "text" {
		t.Fatalf("в «···» шапки скилла нет «Скопировать текст» по имени поля: %+v", head.Menu)
	}
	form := formIn(doc.Components, "skill-form")
	has := false
	for _, f := range form.Fields {
		has = has || f.Name == copyAct.Target
	}
	if !has {
		t.Error("поле, которое копирует кнопка, на экране скилла отсутствует")
	}

	// вес списка не зависит от размера текстов
	baseline := len(stnMarkup(t, mustDoc(t, s, skillsScreen)))
	big := strings.Repeat("Длинная строка инструкции Сёме про сервер и порядок работы. ", 90) // ≈ 5 КБ
	for i := 0; i < 20; i++ {
		k := &store.Skill{Name: fmt.Sprintf("extra-%d", i), Title: fmt.Sprintf("Ещё один %d", i), Group: "Администрирование",
			When: "когда нужно", Enabled: true, Source: store.SkillOwn, Text: big}
		if err := s.eng.SaveSkill(k, "Вадим"); err != nil {
			t.Fatal(err)
		}
	}
	after := mustDoc(t, s, skillsScreen)
	grown := len(stnMarkup(t, after)) - baseline
	// двадцать новых строк — это строки, а не сто килобайт текста
	if perRow := grown / 20; perRow > 1500 {
		t.Errorf("каждая строка списка весит %d байт (текст скилла ≈ 5 КБ): список носит что-то лишнее", perRow)
	}
	if strings.Contains(stnMarkup(t, after), "Длинная строка инструкции") {
		t.Error("текст скилла попал в разметку списка")
	}
}

func mustDoc(t *testing.T, s *Server, screen string) *Document {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	return doc
}

// Строка списка тонкая: название, не больше двух капсул, фраза, тумблер «включён» и
// «···»; вся строка ведёт на экран скилла.
func TestSkillRowsAreThin(t *testing.T) {
	s, _ := stnFix(t)
	doc := mustDoc(t, s, skillsScreen)
	count := 0
	var walk func(cs []Component)
	walk = func(cs []Component) {
		for i := range cs {
			c := cs[i]
			if strings.HasPrefix(c.ID, "skill-") && c.Type == "row" {
				count++
				name := strings.TrimPrefix(c.ID, "skill-")
				if len(c.Badges) > 2 {
					t.Errorf("%s: капсул %d: %+v", name, len(c.Badges), c.Badges)
				}
				if n := stnVisibleButtons(&c); n != 0 {
					t.Errorf("%s: в строке кнопок %d, а действия — тумблер, переход и «···»", name, n)
				}
				var toggle, goTo bool
				for _, a := range c.Actions {
					toggle = toggle || a.Action == "toggle_skill" && a.Label == "toggle"
					goTo = goTo || a.Action == "$goto" && a.Target == engine.SkillScreen(name)
				}
				if !toggle || !goTo || c.Toggle == "" {
					t.Errorf("%s: нет тумблера или перехода на экран скилла: %+v", name, c.Actions)
				}
				for _, a := range c.Menu {
					if a.Action == "$copy" {
						t.Errorf("%s: «Скопировать» с текстом в меню строки", name)
					}
				}
			}
			walk(c.Children)
		}
	}
	walk(doc.Components)
	if count < 6 {
		t.Fatalf("строк скиллов %d, ждали четыре встроенных, свой и из чата", count)
	}
	// «ждёт решения» — первая капсула у скилла с предложением правки, а встроенный без
	// правок называется только размером
	linux := findRow(doc, "skill-linux-admin")
	if len(linux.Badges) != 2 || linux.Badges[0].Text != "ждёт решения" || !strings.HasPrefix(linux.Badges[1].Text, "≈") {
		t.Errorf("капсулы скилла с предложением: %+v", linux.Badges)
	}
	git := findRow(doc, "skill-git-commits")
	if len(git.Badges) != 1 || !strings.HasPrefix(git.Badges[0].Text, "≈") {
		t.Errorf("капсулы встроенного скилла без правок: %+v", git.Badges)
	}
	own := findRow(doc, "skill-backups")
	if len(own.Badges) != 2 || own.Badges[0].Text != "свой" || len(own.Menu) != 1 || own.Menu[0].Action != "delete_skill" || own.Menu[0].Confirm == "" {
		t.Errorf("свой скилл: капсулы %+v, меню %+v", own.Badges, own.Menu)
	}
	if len(findRow(doc, "skill-git-commits").Menu) != 0 {
		t.Error("у встроенного скилла в «···» есть удаление")
	}
	// в шапке раздела одна кнопка «Добавить скилл»; формы добавления под списком нет
	if len(doc.ToolbarActions) != 1 || doc.ToolbarActions[0].Label != "Добавить скилл" || doc.ToolbarActions[0].Target != entityAddr(skillsScreen, skillsNew, "") {
		t.Errorf("кнопки шапки: %+v", doc.ToolbarActions)
	}
	if findRow(doc, "skill-new") != nil {
		t.Error("форма «Добавить скилл» осталась под списком")
	}
	// предел токенов — «Тонкая настройка», и он не потерян
	if got := rareFieldsOf(rareBody(t, s, skillsScreen))["max"]; got.Name == "" {
		t.Error("предел токенов пропал из «Тонкой настройки»")
	}
}

// Экран скилла: форма строками, откуда, польза, «Опасное» последним; сохраняет
// полоса прежним `save_skill`, и сохранение без правки встроенный скилл не трогает.
func TestSkillScreenAndSave(t *testing.T) {
	s, st := stnFix(t)
	// предложение правки видно вверху экрана скилла
	doc := mustDoc(t, s, engine.SkillScreen("linux-admin"))
	if row := findRow(doc, "skill-draft-d1"); row == nil || row.Actions[0].Target != learningScreen || row.Actions[0].Style != "primary" {
		t.Errorf("предложение правки не стоит на экране скилла: %+v", row)
	}
	if doc.Components[0].Type != "crumb" || doc.Components[1].ID != "entity" {
		t.Errorf("шапка сущности не первой: %+v", doc.Components[0])
	}
	// форма: пять полей, действие прежнее, сохраняет полоса
	form := formIn(doc.Components, "skill-form")
	if form == nil || form.Submit.Action != "save_skill" || form.Submit.Target != "linux-admin" || !form.Deferred || len(form.Fields) != 5 {
		t.Fatalf("форма скилла: %+v", form)
	}
	// встроенный, которого не правили: «Опасного» нет (нечего возвращать и нельзя удалить)
	if findRow(doc, "skill-danger") != nil {
		t.Error("у встроенного скилла без правок есть «Опасное»")
	}
	// сохранение без правки скилл не трогает: он остаётся «не правленным» и обновляется из сборки
	unchanged := map[string]string{}
	for _, f := range form.Fields {
		unchanged[f.Name] = f.Value
	}
	if _, err := s.execute(actionRequest{Action: "save_skill", Target: "linux-admin", Screen: engine.SkillScreen("linux-admin"), Params: unchanged}); err != nil {
		t.Fatal(err)
	}
	if k, _ := st.Skill("linux-admin"); k.EditedBy != "" {
		t.Errorf("сохранение без правки пометило встроенный скилл правленным: %q", k.EditedBy)
	}
	// правка встроенного «Коммиты в репозиториях» (в «Linux-сервер» текст с «--disk-usage»
	// не проходит маску «sk-» из «Памяти» — это прежняя беда, см. отчёт): после неё
	// «Опасное» последним, с «Вернуть…» и вопросом, а сборка текст не обновляет
	const git = "git-commits"
	edit := map[string]string{"when": "когда просят закоммитить — по-новому"}
	if next, err := s.execute(actionRequest{Action: "save_skill", Target: git, Screen: engine.SkillScreen(git), Params: edit}); err != nil || next != engine.SkillScreen(git) {
		t.Fatalf("save_skill вернул %q, %v: после сохранения остаёмся на экране скилла", next, err)
	}
	was, _ := engine.BuiltinSkillText(git)
	k, _ := st.Skill(git)
	if k.When != edit["when"] || k.EditedBy != "человек" || k.Title != was.Title || k.Text != was.Text {
		t.Errorf("после правки: %+v", k)
	}
	doc = mustDoc(t, s, engine.SkillScreen(git))
	danger := doc.Components[len(doc.Components)-1]
	if danger.Style != "danger" || len(danger.Children) != 1 || danger.Children[0].Actions[0].Action != "reset_skill" || danger.Children[0].Actions[0].Confirm == "" {
		t.Errorf("«Опасное» у правленного встроенного: %+v", danger)
	}
	if next, err := s.execute(actionRequest{Action: "reset_skill", Target: git, Screen: engine.SkillScreen(git)}); err != nil || next != engine.SkillScreen(git) {
		t.Fatalf("reset_skill: %q %v", next, err)
	}
	if k, _ := st.Skill(git); k.When == edit["when"] || k.EditedBy != "" {
		t.Errorf("«Вернуть встроенный текст» не вернул текст: %+v", k)
	}
	// несуществующее встроенное отвергается словами
	if _, err := s.execute(actionRequest{Action: "reset_skill", Target: "backups"}); err == nil {
		t.Error("у своего скилла нет встроенного текста, а «Вернуть» прошло")
	}
	// свой скилл: «Удалить…» с вопросом последним; вернуть встроенный нечего
	own := mustDoc(t, s, engine.SkillScreen("backups"))
	last := own.Components[len(own.Components)-1]
	if last.Style != "danger" || len(last.Children) != 1 || last.Children[0].Actions[0].Action != "delete_skill" {
		t.Errorf("«Опасное» своего скилла: %+v", last)
	}
	// плохое отвергается и ничего не пишет
	if _, err := s.execute(actionRequest{Action: "save_skill", Target: "backups", Params: map[string]string{"text": "ключ -----BEGIN RSA PRIVATE KEY----- вот"}}); err == nil {
		t.Error("скилл с приватным ключом принят")
	}
	if _, err := s.execute(actionRequest{Action: "save_skill", Target: "нет-такого", Params: edit}); err == nil {
		t.Error("несуществующий скилл сохранён")
	}
	// тумблер на экране скилла не уводит на список, а из «Развития» ведёт, как вёл
	if next, err := s.execute(actionRequest{Action: "toggle_skill", Target: "backups", Screen: engine.SkillScreen("backups")}); err != nil || next != engine.SkillScreen("backups") {
		t.Errorf("toggle_skill с экрана скилла: %q %v", next, err)
	}
	if k, _ := st.Skill("backups"); k.Enabled {
		t.Error("тумблер не выключил скилл")
	}
	if next, _ := s.execute(actionRequest{Action: "toggle_skill", Target: "backups", Screen: learningScreen}); next != skillsScreen {
		t.Errorf("toggle_skill из «Развития» ведёт на %q", next)
	}
	// выключенный — капсула в шапке и главная кнопка «Включить»
	off := mustDoc(t, s, engine.SkillScreen("backups"))
	headRow := off.Components[1].Children[0]
	if len(headRow.Actions) != 1 || headRow.Actions[0].Label != "Выключить" && headRow.Actions[0].Label != "Включить" {
		t.Errorf("главная кнопка шапки: %+v", headRow.Actions)
	}
	// откуда и польза названы
	for _, id := range []string{"skill-source", "skill-size", "skill-uses"} {
		if findRow(own, id) == nil {
			t.Errorf("на экране скилла нет строки %s", id)
		}
	}
}

// «Добавить скилл» — свой экран: форма с прежними полями, после «Добавить» — экран нового
// скилла; имя «new» занято адресом.
func TestSkillAddScreen(t *testing.T) {
	s, st := stnFix(t)
	doc := mustDoc(t, s, entityAddr(skillsScreen, skillsNew, ""))
	form := formIn(doc.Components, "skill-new")
	if form == nil || form.Submit.Action != "add_skill" || form.Deferred {
		t.Fatalf("форма добавления: %+v", form)
	}
	names := []string{}
	for _, f := range form.Fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "name,title,group,keywords,when,text" {
		t.Errorf("поля добавления: %v", names)
	}
	next, err := s.execute(actionRequest{Action: "add_skill", Params: map[string]string{"name": "new-one", "title": "Новый", "group": "Прочее",
		"keywords": "а, б", "when": "когда нужно", "text": "Делай так."}})
	if err != nil || next != engine.SkillScreen("new-one") {
		t.Fatalf("add_skill: %q %v", next, err)
	}
	if k, ok := st.Skill("new-one"); !ok || k.Source != store.SkillOwn || !k.Enabled || k.Title != "Новый" {
		t.Errorf("новый скилл: %+v %v", k, ok)
	}
	for name, p := range map[string]map[string]string{
		"имя new":         {"name": "new", "title": "x", "text": "y"},
		"имя дважды":      {"name": "backups", "title": "x", "text": "y"},
		"имя кириллицей":  {"name": "скилл", "title": "x", "text": "y"},
		"секрет в тексте": {"name": "with-key", "title": "x", "text": "-----BEGIN PRIVATE KEY-----"},
	} {
		if _, err := s.execute(actionRequest{Action: "add_skill", Params: p}); err == nil {
			t.Errorf("%s: принято", name)
		}
	}
	if _, ok := st.Skill("new"); ok {
		t.Error("скилл «new» завёлся: его адрес — экран добавления")
	}
	// из чата блок с именем «new» тоже не заводит такой скилл
	if err := s.eng.SaveSkill(&store.Skill{Name: "new", Title: "x", Text: "y", Enabled: true, Source: store.SkillChat}, "чат"); err == nil {
		t.Error("скилл «new» принят из чата")
	}
}

// Старые адреса и переходы: в уведомлениях и карточках лежат кнопки с прежними именами и
// адресами, и они работают.
func TestSkillOldAddressesAndActions(t *testing.T) {
	s, st := stnFix(t)
	// карточка предложения ведёт на экран скилла через общий помощник
	if got := engine.SkillScreen("linux-admin"); got != "settings:skills@linux-admin" {
		t.Errorf("адрес скилла: %q", got)
	}
	// «Принять» и «Отклонить» предложения: прежнее действие с целью «id~accept»
	if _, err := s.execute(actionRequest{Action: "decide_skill", Target: "d1~reject"}); err != nil {
		t.Fatal(err)
	}
	if d, ok := st.SkillDraft("d1"); !ok || d.State != store.DraftRejected {
		t.Errorf("после «Отклонить»: %+v", d)
	}
	if _, err := s.execute(actionRequest{Action: "decide_skill", Target: "нет-такого~accept"}); err == nil {
		t.Error("решение по несуществующему предложению принято")
	}
	// скилл с кириллицей и пробелом в имени, которого нет, — объяснение, а не ошибка
	for _, screen := range []string{"settings:skills@Нет такого", "settings:skills@linux-admin~любое"} {
		if _, err := s.buildScreen(screen, "", "", 0); err != nil {
			t.Errorf("%s: %v", screen, err)
		}
	}
	gone := mustDoc(t, s, "settings:skills@Нет такого")
	if e := gone.Components[len(gone.Components)-1]; e.Style != "empty" {
		t.Errorf("экран несуществующего скилла: %+v", e)
	}
}

// Адрес скилла строится одним помощником: склейка руками где-то ещё — это адрес, который
// забудут обновить (как у серверов, секретов и агентов).
func TestNoHandWrittenSkillAndStorageAddressesInCode(t *testing.T) {
	var found []string
	for _, root := range []string{"..", "../../cmd"} {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			slash := filepath.ToSlash(path)
			if strings.HasSuffix(slash, "engine/skills.go") {
				return nil // здесь адрес скилла и живёт
			}
			for _, bad := range []string{`"settings:skills@" +`, `"settings:skills@"+`, `skillsScreen + "@" +`, `skillsScreen+"@"+`,
				`"settings:storages@" +`, `"settings:storages@"+`, `storagesScreen + "@" +`, `storagesScreen+"@"+`} {
				if strings.Contains(string(raw), bad) {
					found = append(found, path+": "+bad)
				}
			}
			return nil
		})
	}
	if len(found) > 0 {
		t.Errorf("адреса скиллов и хранилищ склеиваются руками: %v", found)
	}
}
