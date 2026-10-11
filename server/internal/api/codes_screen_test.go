package api

// Коды на экранах: капсула в строке файла, пары «подпись — значение» в
// раскрытии, настройки на «Инструментах». Новых типов компонентов для
// этого не заводится — иначе клиенту понадобилась бы строка в словаре и
// новая версия договора, а капсулы, пары и раскрытие у него давно есть.

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

func fileWithCode(t *testing.T, st *store.Store) int64 {
	t.Helper()
	att := &store.Attachment{Filename: "чек.png", MIME: "image/png", Size: 1024,
		Kind: store.AttKindReceipt, Vendor: "Колеса Даром", Amount: 55830, Currency: "RUB",
		SHA256: "проба", Storage: "local", Key: "пр/об/а"}
	if err := st.InsertAttachment(att); err != nil {
		t.Fatalf("файл: %v", err)
	}
	if err := st.SaveFileCodes(att.ID, []store.FileCode{{
		Symbology: "qr", Value: "t=20260406T1847&s=55830.00&fn=7380440902615257&i=4719&fp=3641530653&n=1",
		Format: store.CodeFNS, Fields: []store.CodeField{
			{Label: "Дата", Value: "06.04.2026 18:47"},
			{Label: "Сумма", Value: "55830.00"},
			{Label: "ФД", Value: "4719"},
		}}}); err != nil {
		t.Fatalf("коды: %v", err)
	}
	return att.ID
}

func TestCodesOnFilesScreen(t *testing.T) {
	s, st := testServer(t)
	id := fileWithCode(t, st)

	doc, err := s.buildScreen("files", "", "", 0)
	if err != nil || doc == nil {
		t.Fatalf("экран «Файлы»: %v", err)
	}
	строка := findRow(doc, "att-"+itoa(id))
	if строка == nil {
		t.Fatal("строки файла нет на экране")
	}
	has := false
	for _, b := range строка.Badges {
		if b.Text == "QR ФНС" {
			has = true
		}
	}
	if !has {
		t.Errorf("нет капсулы «QR ФНС»: %+v", строка.Badges)
	}
	var поля []string
	for _, p := range строка.Props {
		поля = append(поля, p.Label+"="+p.Value)
	}
	свод := strings.Join(поля, " ")
	for _, want := range []string{"ФД=4719", "Сумма=55830.00", "Код=QR"} {
		if !strings.Contains(свод, want) {
			t.Errorf("в раскрытии нет %q: %s", want, свод)
		}
	}
	if !строка.Expandable {
		t.Error("строка с кодами должна раскрываться")
	}
}

// «Искали и не нашли» тоже видно: пустое место в карточке иначе не
// отличить от «даже не смотрели».
func TestCodesNotFoundVisibleInCard(t *testing.T) {
	s, st := testServer(t)
	att := &store.Attachment{Filename: "фото.png", MIME: "image/png", Size: 2048,
		SHA256: "проба2", Storage: "local", Key: "пр/об/а2"}
	if err := st.InsertAttachment(att); err != nil {
		t.Fatalf("файл: %v", err)
	}
	if err := st.SaveFileCodes(att.ID, []store.FileCode{{Format: store.CodeNone,
		Fields: []store.CodeField{{Label: "Искали", Value: "8.0 с"}}}}); err != nil {
		t.Fatalf("отметка: %v", err)
	}
	doc, err := s.buildScreen("files", "", "", 0)
	if err != nil || doc == nil {
		t.Fatalf("экран: %v", err)
	}
	строка := findRow(doc, "att-"+itoa(att.ID))
	if строка == nil {
		t.Fatal("строки файла нет")
	}
	нашли := false
	for _, f := range строка.Facts {
		if f.Label == "Коды" && strings.Contains(f.Value, "не найдены") {
			нашли = true
		}
	}
	if !нашли {
		t.Errorf("в карточке не сказано, что кодов не нашли: %+v", строка.Facts)
	}
	for _, b := range строка.Badges {
		if b.Text == "код" || strings.Contains(b.Text, "ФНС") {
			t.Errorf("у файла без кодов не должно быть капсулы кода: %q", b.Text)
		}
	}
}

// Поиск по экрану идёт в базу: «ФД 4719» должен находить чек так же, как
// имя файла.
func TestCodeSearchOnFilesScreen(t *testing.T) {
	s, st := testServer(t)
	id := fileWithCode(t, st)
	doc, err := s.buildScreen("files", "", "ФД 4719", 0)
	if err != nil || doc == nil {
		t.Fatalf("экран: %v", err)
	}
	if findRow(doc, "att-"+itoa(id)) == nil {
		t.Fatal("по «ФД 4719» чек не нашёлся")
	}
}

// Ни один экран с кодами не приносит типа, которого нет у клиента.
func TestCodesWithoutNewDSLTypes(t *testing.T) {
	s, st := testServer(t)
	fileWithCode(t, st)
	comps, fields := dictionaryTypes(web.Dictionary())
	for _, screen := range []string{"files", "settings:tools"} {
		doc, err := s.buildScreen(screen, "", "", 0)
		if err != nil || doc == nil {
			t.Fatalf("экран %q: %v", screen, err)
		}
		uc, uf := usedTypes(doc)
		for name := range uc {
			if name != "" && !comps[name] {
				t.Errorf("экран %q принёс компонент %q, которого нет в словаре клиента", screen, name)
			}
		}
		for name := range uf {
			if name != "" && !fields[name] {
				t.Errorf("экран %q принёс поле %q, которого нет в словаре клиента", screen, name)
			}
		}
	}
}

// Настройки кодов правятся с экрана, а не в коде: виды — тумблерами свёрнутого
// раздела «Коды на картинках» на экране «Инструментов» (вкладок у него больше нет),
// пределы времени и размера — полями «Тонкой настройки».
func TestCodesSettingsOnTools(t *testing.T) {
	s, st := testServer(t)
	doc, err := s.buildScreen(toolsScreen, "", "", 0)
	if err != nil || doc == nil {
		t.Fatalf("экран: %v", err)
	}
	section := findSectionByID(doc.Components, "codes")
	if section == nil {
		t.Fatal("на «Инструментах» нет раздела про коды")
	}
	if !section.Collapsed || section.Expanded {
		t.Errorf("«Коды на картинках» должны быть свёрнутым разделом: свёрнут %v, раскрыт %v", section.Collapsed, section.Expanded)
	}
	тумблеры, списки := 0, 0
	for _, row := range section.Children {
		if row.Toggle != "" {
			тумблеры++
		}
		списки += len(row.Selects)
	}
	if тумблеры < 3 {
		t.Errorf("видов кодов с тумблером %d — их должно быть видно все", тумблеры)
	}
	if списки != 0 {
		t.Errorf("пределы переехали в «Тонкую настройку», а в разделе осталось %d списков", списки)
	}
	// адрес прежней вкладки открывает тот же экран с раскрытым разделом
	old, err := s.buildScreen(toolsScreen+"~"+toolsTabCodes, "", "", 0)
	if err != nil || old == nil {
		t.Fatalf("прежний адрес вкладки: %v", err)
	}
	if opened := findSectionByID(old.Components, "codes"); opened == nil || !opened.Expanded {
		t.Errorf("прежний адрес «~codes» должен раскрывать раздел про коды: %+v", opened)
	}
	if len(old.Subtabs) != 0 {
		t.Errorf("у «Инструментов» внутренних вкладок больше нет, а их %d", len(old.Subtabs))
	}
	// пределы — поля «Тонкой настройки»: сохраняются и читаются движком
	fields := rareFieldsOf(rareBody(t, s, toolsScreen))
	if fields["limit_s"].Name == "" || fields["max_mp"].Name == "" {
		t.Fatalf("в «Тонкой настройке» нет пределов кодов: %v", fields)
	}
	if fields["limit_s"].Value != "8" || fields["max_mp"].Value != "40" {
		t.Errorf("умолчания на экране: %q и %q", fields["limit_s"].Value, fields["max_mp"].Value)
	}
	if _, err := s.execute(actionRequest{Action: "set_codes_limits", Params: map[string]string{"limit_s": "15", "max_mp": "80"}}); err != nil {
		t.Fatal(err)
	}
	if s.eng.CodesLimit().Seconds() != 15 || s.eng.PixelsLimit() != 80 {
		t.Errorf("движок читает %v и %d", s.eng.CodesLimit(), s.eng.PixelsLimit())
	}
	// плохое число отвергается и ничего не пишет; непришедшее поле соседа не сбрасывает
	for _, bad := range []map[string]string{{"limit_s": "0"}, {"limit_s": "много"}, {"max_mp": "999"}, {"limit_s": "20", "max_mp": "x"}} {
		if _, err := s.execute(actionRequest{Action: "set_codes_limits", Params: bad}); err == nil {
			t.Errorf("%v принято", bad)
		}
	}
	if s.eng.CodesLimit().Seconds() != 15 {
		t.Errorf("отвергнутая форма что-то записала: %v", s.eng.CodesLimit())
	}
	if _, err := s.execute(actionRequest{Action: "set_codes_limits", Params: map[string]string{"max_mp": "60"}}); err != nil {
		t.Fatal(err)
	}
	if s.eng.CodesLimit().Seconds() != 15 || s.eng.PixelsLimit() != 60 {
		t.Errorf("непришедшее поле сбросило соседа: %v %d", s.eng.CodesLimit(), s.eng.PixelsLimit())
	}
	// прежние действия со списками «15 с» и «40 Мп» работают как работали
	if _, err := s.execute(actionRequest{Action: "save_codes_limit", Params: map[string]string{"limit": "30 с"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.execute(actionRequest{Action: "save_codes_pixels", Params: map[string]string{"limit": "20 Мп"}}); err != nil {
		t.Fatal(err)
	}
	if st.Setting("codes.max_s", "") != "30" || st.Setting("codes.max_mp", "") != "20" {
		t.Errorf("прежние действия записали %q и %q", st.Setting("codes.max_s", ""), st.Setting("codes.max_mp", ""))
	}
}

func findSectionByID(cs []Component, id string) *Component {
	for i := range cs {
		if cs[i].Type == "section" && cs[i].ID == id {
			return &cs[i]
		}
		if c := findSectionByID(cs[i].Children, id); c != nil {
			return c
		}
	}
	return nil
}
