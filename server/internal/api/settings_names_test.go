package api

import "testing"

// Имя раздела в колонке и заголовок его экрана — одно слово: «Вход и доступ»
// в колонке и «Доступ» над содержимым читались как два разных места
// (`design/nastroyki-2/README.md`, «Наблюдения мимоходом»). Тест перебирает
// колонку целиком, а не названные разделы: новый раздел с двумя именами
// роняет его сам.
func TestSectionNameInColumnIsScreenHeading(t *testing.T) {
	s, _ := testServer(t)
	addrs := []string{settingsRoot}
	for _, group := range settingsGroups {
		addrs = append(addrs, group.Sections...)
	}
	for _, addr := range addrs {
		doc, err := s.buildScreen(addr, "", "", 0)
		if err != nil {
			t.Errorf("экран %s: %v", addr, err)
			continue
		}
		if want := sectionTitle[addr]; doc.Heading != want {
			t.Errorf("раздел %s: в колонке %q, а заголовок экрана %q", addr, want, doc.Heading)
		}
	}
}

// Строка раздела на «Обзоре» и в поиске зовётся так же, как в колонке.
func TestSectionNameInOverviewIsColumnName(t *testing.T) {
	s, _ := testServer(t)
	for _, section := range s.allSections() {
		addr := baseScreen(section.Screen)
		if want, ok := sectionTitle[addr]; !ok || section.Title != want {
			t.Errorf("раздел %s: в колонке %q, а на «Обзоре» %q", addr, want, section.Title)
		}
	}
}
