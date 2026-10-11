package api

// Тело письма как HTML — своим адресом и в песочнице.
//
// Сервер отдавал `detailHtml` в описании экрана с пометкой «переключатель
// в клиенте», но клиент, у которого этот переключатель был, сняли 8
// сентября 2026. С тех пор 1321 письмо из 1704 человек видел текстовым
// запасным вариантом — у рассылок он часто нечитаем. Это та же беда, что
// со сжатием: клиента сняли, а возможность осталась на мёртвой дороге.
//
// Почему отдельный адрес, а не вставка в страницу. Вставленная разметка
// живёт в адресе Сёмы: её CSS перекрашивает интерфейс, её ссылки ведут
// по её путям, а любая дыра в очистке становится дырой в самой Сёме.
// Отдельный документ в `<iframe sandbox>` живёт в непрозрачном источнике:
// у него нет доступа ни к cookie, ни к странице, ни к сети.
//
// Стен две, и вторая не заменяет первую: разметка чистится
// (`internal/htmlsafe`) и только потом отдаётся с заголовками, которые
// запрещают всё остальное.

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/htmlsafe"
)

// messagePath — начало адреса тела письма. Знают о нём двое: маршрут и
// заголовки безопасности, и разойтись им нельзя.
const messagePath = "/письмо"

// handleMessageBody — GET /письмо/{id} : тело письма как отдельный
// документ. Параметр `картинки=1` — человек нажал «Показать картинки».
func (s *Server) handleMessageBody(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if _, err := s.eng.Store().Message(id); err != nil {
		http.Error(w, "письма нет", http.StatusNotFound)
		return
	}
	bodies, err := s.eng.Store().Bodies([]int64{id})
	if err != nil {
		http.Error(w, "тело письма недоступно", http.StatusNotFound)
		return
	}
	исходный := bodies[id].HTML
	if strings.TrimSpace(исходный) == "" {
		http.Error(w, "у письма нет HTML-версии", http.StatusNotFound)
		return
	}
	картинки := r.URL.Query().Get("картинки") == "1"
	var и htmlsafe.Result
	// перевод показывается тем же письмом: вёрстка, картинки и порядок
	// прежние, меняются только слова
	if r.URL.Query().Get("перевод") == "1" {
		if t, ok := s.eng.MessageTranslation(id); ok && strings.TrimSpace(t.HTML) != "" {
			и = htmlsafe.Clean(t.HTML, картинки)
		} else {
			http.Error(w, "перевода этого письма пока нет", http.StatusNotFound)
			return
		}
	} else {
		и = htmlsafe.Clean(исходный, картинки)
	}

	// картинки разрешаются заголовком, а не только очисткой: даже если
	// адрес просочится мимо разбора, браузер за ним не пойдёт
	img := "data:"
	if картинки {
		img = "data: https: http:"
	}
	// `sandbox` тут же, в CSP: рамка на странице может быть подменена
	// правкой разметки, а заголовок ответа — нет
	w.Header().Set("Content-Security-Policy", strings.Join([]string{
		// allow-scripts нужен ровно одному скрипту — нашему, который
		// сообщает высоту. Чужие не выполнятся дважды: разбор вырезает
		// все `<script>` и все `on*`, а CSP пускает только скрипт с
		// одноразовым ключом, которого в письме взяться неоткуда.
		// Источник при этом остаётся непрозрачным (allow-same-origin нет),
		// поэтому даже выполнившийся скрипт не видит ни cookie Сёмы, ни
		// её страницу
		"default-src 'none'",
		// скриптов в письме нет вовсе. Свой скрипт (он умел сообщать
		// высоту наверх) убран нарочно: ради удобства пришлось бы
		// включить `allow-scripts`, а это стена, которую тогда держал бы
		// один одноразовый ключ. Высоту рамки человек тянет сам —
		// дешёвая цена за то, что в письме не исполняется ничего
		"sandbox allow-popups allow-popups-to-escape-sandbox",
		"img-src " + img,
		"script-src 'none'",
		"style-src 'unsafe-inline'",
		"font-src data:",
		"form-action 'none'",
		"base-uri 'none'",
		// рамку с письмом ставит только сама Сёма: чужой сайт вставить
		// её к себе не может. Это то же, что делал «X-Frame-Options», но
		// точно — не запрещая нам показывать письмо у себя
		"frame-ancestors 'self'",
	}, "; "))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="ru"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(`<style>` + messageStyles + `</style></head><body>`)
	// письмо, собранное из одних картинок, без них выглядит пустым — и
	// человек решает, что сломалась Сёма, а не что картинки не загружены.
	// Полоса сверху объясняет пустоту прямо в самой рамке
	if и.Images > 0 && !картинки {
		b.WriteString(`<div class="сёма-полоса">` +
			html.EscapeString(fmt.Sprintf("Картинки не загружены — в письме их %d. Пока браузер за ними не сходил, отправитель не знает, что письмо открыли. Кнопка «Показать картинки» — над рамкой.", и.Images)) +
			`</div>`)
	}
	b.WriteString(и.HTML)
	b.WriteString(`</body></html>`)
	_, _ = w.Write([]byte(b.String()))
}

// messageStyles — наши правила ПЕРЕД правилами письма: письмо их
// переопределит, а мы задаём поля, шрифт и то, чтобы широкая таблица
// рассылки не растягивала рамку вбок.
const messageStyles = `
.сёма-полоса{font:12px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
  background:#FBF0DC;color:#6b4a00;border:1px solid #e8d9b5;border-radius:8px;
  padding:8px 10px;margin:0 0 10px}
@media (prefers-color-scheme: dark){.сёма-полоса{background:#3a2f16;color:#f0dfae;border-color:#5a4a22}}
html,body{margin:0;padding:0;background:#fff;color:#1c1c1e}
body{font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;padding:12px;
  overflow-x:auto;word-break:break-word;overflow-wrap:anywhere}
img{max-width:100%;height:auto}
table{max-width:100%}
a{color:#0a84ff}
@media (prefers-color-scheme: dark){
  html,body{background:#1c1c1e;color:#f2f2f7}
  a{color:#64d2ff}
}
`

// messageHasHTML — есть ли у письма HTML-версия. Экран спрашивает это,
// чтобы не рисовать переключатель там, где переключать не на что.
func (s *Server) messageHasHTML(id int64) bool {
	bodies, err := s.eng.Store().Bodies([]int64{id})
	if err != nil {
		return false
	}
	return strings.TrimSpace(bodies[id].HTML) != ""
}

// messageBodyAddr — путь к рамке. Отдельная функция, потому что его знают
// и экран, и клиент, и тест.
func messageBodyAddr(id int64, картинки bool) string {
	if картинки {
		return fmt.Sprintf("%s/%d?картинки=1", messagePath, id)
	}
	return fmt.Sprintf("%s/%d", messagePath, id)
}
