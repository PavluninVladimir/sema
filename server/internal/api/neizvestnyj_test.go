package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Ни на одном экране не должно быть слов «неизвестный компонент».
//
// 11 сентября 2026 на «Файлах» вместо «260 файлов · 31.9 МБ · 4
// источника» стояло три раза «неизвестный компонент», и то же самое на
// «Модулях». Договор словаря тут ни при чём: у детей блока «числа» вида
// нет вовсе — число и подпись несёт ребёнок, а разметку даёт родитель.
// Converter считал пустой вид незнакомым и затирал значение словами об
// ошибке. То есть портил данные, которых не понял, вместо того чтобы их
// не трогать.
//
// Тест ловит это на собранной разметке, а не на дереве компонентов:
// именно там видно, что человек прочтёт.
func TestNoScreenHasUnknownComponent(t *testing.T) {
	s, _ := testServer(t)
	screens := []string{
		"files", "modules", "sent", "health", "inbox", "tasks",
		"memory", "settings", "settings:tools", "settings:agents", "notifications",
	}
	for _, screen := range screens {
		doc, err := s.buildScreen(screen, "", "", 50)
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		html, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatalf("%s: %v", screen, err)
		}
		if strings.Contains(string(html), "неизвестный компонент") {
			t.Errorf("%s: на экране стоит «неизвестный компонент» вместо значения", screen)
		}
	}
}

// А настоящий незнакомый вид по-прежнему называет себя: молчаливое
// пустое место было бы хуже — новый тип на сервере без строки у клиента
// должен быть заметен сразу.
func TestRealUnknownKindNamesItself(t *testing.T) {
	б := toBlock(Component{Type: "выдуманный-вид", Text: "что-то"})
	if !strings.Contains(б.Text, "неизвестный компонент") {
		t.Errorf("незнакомый вид промолчал: %+v", б)
	}
	// а носитель значения внутри «чисел» значение сохраняет
	ч := toBlock(Component{Text: "260", Title: "файлов"})
	if ч.Text != "260" || ч.Title != "файлов" {
		t.Errorf("значение внутри «чисел» испорчено: текст %q, подпись %q", ч.Text, ч.Title)
	}
}
