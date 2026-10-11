package engine

// Разовая починка превью писем. Превью чистилось от тегов, но не от
// границ частей и заголовков, а содержимое <style> после снятия тегов
// оставалось текстом. Так в списке писем, в контексте модели и в векторе
// поиска оказывались «----boundary_… Content-Type: text/html» и
// «@viewport {…}» вместо письма. Чистка исправлена в источнике, а это —
// уже сохранённое.

import (
	"log"

	"github.com/PavluninVladimir/sema/server/internal/mail"
)

const snippetRepairKey = "mail.snippets_clean"

func (e *Engine) repairSnippets() {
	// двенадцатый проход: чистка научилась снимать комментарии и набивку
	// сущностями, значит по сохранённому надо пройти ещё раз.
	// Предыдущий: пустой результат тоже записывается — раньше превью,
	// от которого после чистки не оставалось ни буквы, сохраняло исходный
	// CSS, и в списке стояло «@media (prefers-color-scheme: dark) {…»
	if e.store.Setting(snippetRepairKey, "") == "14" {
		return
	}
	rows, err := e.store.DirtySnippets(20000)
	if err != nil {
		log.Printf("превью писем: %v", err)
		return
	}
	fixed := 0
	for id, raw := range rows {
		clean := mail.CleanSnippet(raw)
		if clean == raw {
			continue
		}
		// пусто — честнее, чем разметка: в списке останется тема письма
		if err := e.store.SetSnippet(id, clean); err != nil {
			log.Printf("превью письма %d: %v", id, err)
			continue
		}
		fixed++
	}
	if fixed > 0 {
		log.Printf("превью писем: почищено %d из %d", fixed, len(rows))
	}
	_ = e.store.SetSetting(snippetRepairKey, "14")
}
