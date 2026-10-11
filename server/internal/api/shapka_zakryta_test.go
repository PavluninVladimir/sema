package api

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/web"
	"golang.org/x/net/html"
)

// Шапка экрана закрывается там, где положено, — на любом экране, с
// вкладками и без. 13 сентября 2026 закрывающие теги шапки оказались
// внутри условия «есть вкладки»: у чатов, задач и всех прочих экранов
// без вкладок лента, поле ввода и плашка попали внутрь шапки, поле ввода
// уехало вправо, прокрутка пропала. Тесты в Chrome этого не заметили:
// они смотрели на «Почту», где вкладки есть.
func TestHeaderClosedOnEveryScreen(t *testing.T) {
	s, _ := testServer(t)
	for _, screen := range []string{"chats", "tasks", "inbox", "settings", "health", "files"} {
		doc, err := s.buildScreen(screen, "", "", 50)
		if err != nil {
			t.Fatal(screen, err)
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatal(screen, err)
		}
		дерево, err := html.Parse(strings.NewReader("<div id=\"window\">" + string(markup) + "</div>"))
		if err != nil {
			t.Fatal(screen, err)
		}
		var шапка, лента, управление, полоса *html.Node
		var walk func(n *html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode {
				switch {
				case attr(n, "id") == "feed" && лента == nil:
					лента = n
				case hasClass(n, "header") && шапка == nil:
					шапка = n
				case hasClass(n, "controls") && управление == nil:
					управление = n
				case hasClass(n, "tabs-bar") && полоса == nil:
					полоса = n
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(дерево)
		if шапка == nil || лента == nil {
			t.Fatalf("%s: нет шапки или ленты", screen)
		}
		if inside(лента, шапка) {
			t.Errorf("%s: лента оказалась внутри шапки — шапка не закрыта", screen)
		}
		if управление == nil || управление.Parent != шапка {
			t.Errorf("%s: ряд кнопок не прямой потомок шапки", screen)
		}
		if len(doc.Tabs) > 0 {
			if полоса == nil || полоса.Parent != шапка {
				t.Errorf("%s: полоса вкладок не прямой потомок шапки", screen)
			} else if inside(управление, полоса) {
				t.Errorf("%s: кнопки шапки попали внутрь полосы вкладок", screen)
			}
		} else if полоса != nil {
			t.Errorf("%s: полоса вкладок без вкладок", screen)
		}
	}
}

// Место под «···» в ряду кнопок занято всегда: у «Отправителей» на «В
// спаме» меню есть, на «Белом списке» нет, и «Разрешить адрес» ездила на
// ширину кнопки от одного нажатия чипа.
func TestHeaderMenuSlotAlwaysReserved(t *testing.T) {
	s, _ := testServer(t)
	for _, п := range [][2]string{{"senders", "spam"}, {"senders", "ham"}, {"inbox", ""}} {
		doc, err := s.buildScreen(п[0], п[1], "", 50)
		if err != nil {
			t.Fatal(п, err)
		}
		if len(doc.ToolbarActions) == 0 && len(doc.HeaderSelects) == 0 {
			continue
		}
		markup, err := web.Render(toScreen(doc))
		if err != nil {
			t.Fatal(п, err)
		}
		дерево, _ := html.Parse(strings.NewReader("<div id=\"window\">" + string(markup) + "</div>"))
		var ряд *html.Node
		var walk func(n *html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode && hasClass(n, "controls") && ряд == nil {
				ряд = n
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(дерево)
		if ряд == nil {
			t.Fatalf("%v: ряда кнопок нет", п)
		}
		var last *html.Node
		for c := ряд.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				last = c
			}
		}
		if last == nil || !(hasClass(last, "row-menu") || hasClass(last, "more-slot")) {
			t.Errorf("%v: последним в ряду кнопок стоит не «···» и не его место", п)
		}
	}
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, класс string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == класс {
			return true
		}
	}
	return false
}

func inside(n, предок *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p == предок {
			return true
		}
	}
	return false
}
