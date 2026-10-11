package web

// Значки интерфейса. Сервер называет их именами SF Symbols — так они
// приходят в UI-DSL и так же назывались в снятом клиенте на маке. В
// браузере SF Symbols нарисовать нечем, поэтому здесь лежит свой набор:
// одна сетка 16×16, обводка 1.5, скруглённые концы, цвет наследуется от
// текста. Значка нет в наборе — пункт остаётся без значка: пустой кружок
// вместо смысла хуже, чем ничего.

import "html/template"

// iconPaths — путь внутри viewBox="0 0 16 16". Имена те же, что шлёт сервер.
var iconPaths = map[string]string{
	// почта и файлы
	"tray.full":      `<path d="M1.5 9.5h3l1 2h5l1-2h3"/><path d="M2.5 3.5h11a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1z"/>`,
	"envelope":       `<path d="M1.5 4.5h13v8h-13z"/><path d="M1.5 5l6.5 4.5L14.5 5"/>`,
	"archivebox":     `<path d="M1.5 3.5h13v3h-13z"/><path d="M2.5 6.5v6h11v-6"/><path d="M6.5 9h3"/>`,
	"trash":          `<path d="M3 4.5h10"/><path d="M4.5 4.5l.7 8.2a1 1 0 0 0 1 .8h3.6a1 1 0 0 0 1-.8l.7-8.2"/><path d="M6.5 4.5v-1.5a1 1 0 0 1 1-1h1a1 1 0 0 1 1 1v1.5"/>`,
	"doc":            `<path d="M4 1.5h5l3.5 3.5v9a1 1 0 0 1-1 1h-7.5a1 1 0 0 1-1-1v-11.5a1 1 0 0 1 1-1z"/><path d="M9 1.5V5h3.5"/>`,
	"arrow.down.doc": `<path d="M4 1.5h5l3.5 3.5v9a1 1 0 0 1-1 1h-7.5a1 1 0 0 1-1-1v-11.5a1 1 0 0 1 1-1z"/><path d="M8 6v5"/><path d="M6 9l2 2 2-2"/>`,
	"folder":         `<path d="M1.5 4.5h4.5l1.5 2h7v7h-13z"/>`,
	"paperclip":      `<path d="M10.5 5.5l-4.6 4.6a1.6 1.6 0 0 0 2.3 2.3l5-5a3 3 0 0 0-4.3-4.3l-5 5"/>`,
	// разговоры
	"bubble.left":                       `<path d="M2 3.5h12v7.5h-6.5L4.5 14v-3H2v-7.5z"/>`,
	"bubble.left.and.text.bubble.right": `<path d="M2 3.5h9v6H5l-3 2.5z"/><path d="M11 6.5h3v6l-2-1.5H7"/>`,
	"paperplane":                        `<path d="M14.5 1.5L7 9"/><path d="M14.5 1.5l-5 13-2.5-5.5-5.5-2.5z"/>`,
	// разговоры по виду: мессенджер, человек, группа, тема, канал, задача
	"logo.telegram": `<path d="M13.5 3L2.5 7.3l3.4 1.2 1.3 4 1.9-2.2 3.1 2.3z"/><path d="M5.9 8.5l6-4.3"/>`,
	"logo.max":      `<circle cx="8" cy="8" r="4.4"/>`,
	"person.2":      `<circle cx="6" cy="5.5" r="2.2"/><path d="M2 13c0-2.2 1.8-3.8 4-3.8s4 1.6 4 3.8"/><path d="M10.5 3.6a2.1 2.1 0 1 1 0 4"/><path d="M11.8 9.4c1.3.4 2.2 1.8 2.2 3.6"/>`,
	"number":        `<path d="M6.2 2.5l-1.4 11"/><path d="M11.2 2.5l-1.4 11"/><path d="M2.5 6h11"/><path d="M2 10.5h11"/>`,
	"list.bullet":   `<path d="M6 4h8"/><path d="M6 8h8"/><path d="M6 12h8"/><path d="M2.6 4h.1"/><path d="M2.6 8h.1"/><path d="M2.6 12h.1"/>`,
	"megaphone":     `<path d="M2.5 6.5v3h2l6 3v-9l-6 3z"/><path d="M4.5 9.5l1 3.5"/><path d="M12.8 6.3a2.2 2.2 0 0 1 0 3.4"/>`,
	"at":            `<circle cx="8" cy="8" r="2.6"/><path d="M10.6 5.5V9a1.9 1.9 0 0 0 3.4 1.1A6.3 6.3 0 1 0 11 13.5"/>`,
	"cube":          `<path d="M8 1.5l5.8 3.2v6.6L8 14.5l-5.8-3.2V4.7z"/><path d="M2.2 4.7L8 8l5.8-3.3"/><path d="M8 8v6.5"/>`,
	// работа Сёмы
	"clock": `<circle cx="8" cy="8" r="6.2"/><path d="M8 4.5V8l2.5 1.8"/>`,
	"chevron.left.forwardslash.chevron.right": `<path d="M5.5 4.5L2.5 8l3 3.5"/><path d="M10.5 4.5L13.5 8l-3 3.5"/>`,
	"terminal":              `<path d="M1.5 2.5h13v11h-13z"/><path d="M4 6l2.5 2L4 10"/><path d="M8.5 10.5h3.5"/>`,
	"puzzlepiece.extension": `<path d="M6 2.5h4v2a1.3 1.3 0 1 0 2.6 0V2.5"/><path d="M2.5 6v4h2a1.3 1.3 0 1 1 0 2.6H2.5"/><path d="M2.5 6h3.5V2.5"/><path d="M6 13.5h7.5V6"/>`,
	"brain":                 `<path d="M8 3v10"/><path d="M8 4.2a2.2 2.2 0 1 0-3.4 1.9A2 2 0 0 0 3.5 9a2 2 0 0 0 1.6 2 2 2 0 0 0 2.9 1.6"/><path d="M8 4.2a2.2 2.2 0 1 1 3.4 1.9A2 2 0 0 1 12.5 9a2 2 0 0 1-1.6 2 2 2 0 0 1-2.9 1.6"/>`,
	"waveform.path.ecg":     `<path d="M1.5 8.5h3l1.5-4 2.5 8 1.5-4h4.5"/>`,
	"circle.hexagongrid":    `<circle cx="8" cy="3.6" r="1.7"/><circle cx="3.6" cy="10.4" r="1.7"/><circle cx="12.4" cy="10.4" r="1.7"/><path d="M8 5.3v5.4"/><path d="M6.6 4.6L5 9.4"/><path d="M9.4 4.6L11 9.4"/>`,
	"book.closed":           `<path d="M4 1.5h9v13H4a1.5 1.5 0 0 1-1.5-1.5V3A1.5 1.5 0 0 1 4 1.5z"/><path d="M2.5 12.5H13"/>`,
	// настройки и доступ
	"gearshape":   `<circle cx="8" cy="8" r="2.2"/><path d="M8 1.8v1.6M8 12.6v1.6M14.2 8h-1.6M3.4 8H1.8M12.4 3.6l-1.1 1.1M4.7 11.3l-1.1 1.1M12.4 12.4l-1.1-1.1M4.7 4.7L3.6 3.6"/>`,
	"key":         `<circle cx="5" cy="11" r="2.6"/><path d="M6.9 9.1L13 3"/><path d="M11 5l1.6 1.6"/>`,
	"lock":        `<path d="M4 7.2h8v6.3H4z"/><path d="M5.8 7.2V5.4a2.2 2.2 0 0 1 4.4 0v1.8"/>`,
	"hand.raised": `<path d="M6 8V3.6a1.1 1.1 0 0 1 2.2 0V8"/><path d="M8.2 7.6V4.4a1.1 1.1 0 0 1 2.2 0V9"/><path d="M10.4 8.4a1.1 1.1 0 0 1 2.2 0v2.2A3.9 3.9 0 0 1 8.7 14.5H8a4 4 0 0 1-4-4V8.6a1.1 1.1 0 0 1 2-.6"/>`,
	"house":       `<path d="M2 7.5L8 2.5l6 5"/><path d="M3.5 7v6.5h9V7"/>`,
	"server.rack": `<path d="M2 2.8h12v4H2z"/><path d="M2 9.2h12v4H2z"/><path d="M4.4 4.8h.01M4.4 11.2h.01"/>`,
	"network":     `<circle cx="8" cy="8" r="6.2"/><path d="M1.8 8h12.4"/><path d="M8 1.8c1.8 2 2.7 4 2.7 6.2S9.8 12.2 8 14.2C6.2 12.2 5.3 10.2 5.3 8S6.2 3.8 8 1.8z"/>`,
	"bell":        `<path d="M8 2.2a4 4 0 0 0-4 4v2.6L2.8 11h10.4L12 8.8V6.2a4 4 0 0 0-4-4z"/><path d="M6.6 12.6a1.5 1.5 0 0 0 2.8 0"/>`,
	"circle.fill": `<circle cx="8" cy="8" r="4" fill="currentColor" stroke="none"/>`,
	// экраны и разделы
	"magnifyingglass":          `<circle cx="7" cy="7" r="4.5"/><path d="M10.4 10.4l3.1 3.1"/>`,
	"person":                   `<circle cx="8" cy="5.2" r="2.7"/><path d="M2.8 14c0-2.9 2.3-4.6 5.2-4.6s5.2 1.7 5.2 4.6"/>`,
	"rublesign.circle":         `<circle cx="8" cy="8" r="6.2"/><path d="M6.2 11.6V4.6h2.3a2 2 0 0 1 0 4H5.4"/><path d="M5.4 10.2h3.2"/>`,
	"sparkles":                 `<path d="M5.5 2.2l.9 2.4 2.4.9-2.4.9-.9 2.4-.9-2.4-2.4-.9 2.4-.9z"/><path d="M11.2 8.6l.6 1.6 1.6.6-1.6.6-.6 1.6-.6-1.6-1.6-.6 1.6-.6z"/>`,
	"tag":                      `<path d="M2.5 2.5h5l6 6-5 5-6-6z"/><circle cx="5.2" cy="5.2" r="1"/>`,
	"externaldrive":            `<path d="M1.8 4.5h12.4v7H1.8z"/><path d="M4.2 9h.01M11.8 9h2"/>`,
	"link":                     `<path d="M6.6 9.4a2.6 2.6 0 0 0 3.7 0l2.1-2.1a2.6 2.6 0 1 0-3.7-3.7l-1 1"/><path d="M9.4 6.6a2.6 2.6 0 0 0-3.7 0L3.6 8.7a2.6 2.6 0 1 0 3.7 3.7l1-1"/>`,
	"graduationcap":            `<path d="M8 3L1.8 6 8 9l6.2-3z"/><path d="M4.4 7.4v3.4c0 .9 1.6 1.7 3.6 1.7s3.6-.8 3.6-1.7V7.4"/>`,
	"checkmark.seal.fill":      `<path d="M8 1.6l1.6 1.2 2-.2.6 1.9 1.7 1.1-.8 1.8.8 1.8-1.7 1.1-.6 1.9-2-.2L8 13.4l-1.6-1.4-2 .2-.6-1.9-1.7-1.1.8-1.8-.8-1.8 1.7-1.1.6-1.9 2 .2z"/><path d="M5.6 8.2l1.7 1.7 3.2-3.6"/>`,
	"checkmark":                `<path d="M3 8.6l3.2 3.2L13 4.8"/>`,
	"timer":                    `<circle cx="8" cy="9" r="5.4"/><path d="M8 6.2V9l1.9 1.4"/><path d="M6.2 1.8h3.6"/>`,
	"exclamationmark.triangle": `<path d="M8 2.2l6 10.6H2z"/><path d="M8 6.4v3.2"/><path d="M8 11.4h.01"/>`,
	"lightbulb":                `<path d="M6 12.5h4"/><path d="M6.5 14.5h3"/><path d="M8 1.8a4.2 4.2 0 0 0-2.5 7.6c.4.3.6.8.6 1.3v.3h3.8v-.3c0-.5.2-1 .6-1.3A4.2 4.2 0 0 0 8 1.8z"/>`,
	"tray":                     `<path d="M1.5 9.5h3l1 2h5l1-2h3"/><path d="M2.5 3.5h11a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1z"/>`,
	"ellipsis":                 `<circle cx="3.5" cy="8" r="1.2" fill="currentColor" stroke="none"/><circle cx="8" cy="8" r="1.2" fill="currentColor" stroke="none"/><circle cx="12.5" cy="8" r="1.2" fill="currentColor" stroke="none"/>`,
	"questionmark.circle":      `<circle cx="8" cy="8" r="6.2"/><path d="M6.3 6.2a1.8 1.8 0 1 1 2.4 1.7c-.5.2-.7.6-.7 1.1v.4"/><path d="M8 12h.01"/>`,
	"hand.thumbsup":            `<path d="M5.5 14V7.2l2.6-4.7a1.3 1.3 0 0 1 2.4.9L10 6.4h3.2a1.3 1.3 0 0 1 1.2 1.7l-1.3 4.6a1.6 1.6 0 0 1-1.5 1.1H5.5z"/><path d="M2 7.2h3.5V14H2z"/>`,
	"xmark.bin":                `<path d="M3 4.5h10"/><path d="M4.5 4.5l.7 8.2a1 1 0 0 0 1 .8h3.6a1 1 0 0 0 1-.8l.7-8.2"/><path d="M6.6 7.4l2.8 3.2M9.4 7.4l-2.8 3.2"/>`,
	"wrench.and.screwdriver":   `<path d="M2.5 4.2a2.6 2.6 0 0 0 3.4 3.4l6 6 1.6-1.6-6-6A2.6 2.6 0 0 0 4.1 2.6z"/><path d="M11.5 2.5l2 2-2.5 2.5-2-2z"/>`,
}

// Icon — svg по имени SF Symbol; пустая строка, если такого нет.
func Icon(name, class string) template.HTML {
	d, ok := iconPaths[name]
	if !ok || name == "" {
		return ""
	}
	return template.HTML(`<svg class="` + template.HTMLEscapeString(class) +
		`" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" ` +
		`stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + d + `</svg>`)
}

// HasIcon — знает ли набор такое имя. Нужно шаблону: у пункта без
// значка отступ считается иначе, чтобы подписи стояли в одну линию.
func HasIcon(name string) bool {
	_, ok := iconPaths[name]
	return ok
}
