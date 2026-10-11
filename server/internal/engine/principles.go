package engine

// Принципы проекта из CLAUDE.md — привязки уровня папки или файла.
//
// Абзацы «Архитектуры» и «Правил проекта» называют пути в коде, и это
// знание должно приходить в карточку файла само, без ручной разметки.
// Файл инструкций видит только житель машины: Сёма живёт в контейнере и
// репозитория не читает. Поэтому разбирает житель, а Сёма хранит ссылку
// — одну строку итога, раздел и отпечаток абзаца, не сам абзац.
//
// По отпечатку решается всё: тот же — ничего; новый при старом заголовке
// — новая версия записи с `supersedes`; заголовка больше нет — привязки
// помечаются устаревшими, а не удаляются молча. Снятое человеком помнится
// по паре «заголовок + отпечаток» в `outside_acts` и не заводится снова,
// пока абзац не изменился.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// PrincipleSource — приставка источника у привязок из инструкций: по ней
// они отличимы от уроков и требований и снимаются с экрана «Память».
const PrincipleSource = "CLAUDE.md · "

// principleArea — область, в которой живут принципы проекта.
const principleArea = "код"

// PrincipleItem — один путь одного абзаца, как его прислал житель машины.
type PrincipleItem struct {
	Repo    string `json:"repo,omitempty"` // пусто — репозиторий пачки
	Dir     string `json:"dir,omitempty"`
	File    string `json:"file,omitempty"`
	Heading string `json:"heading"`
	Hash    string `json:"hash"`
	Text    string `json:"text"`
}

// principleKey — ключ снятой человеком привязки в outside_acts.
func principleKey(heading, hash string) string {
	return "principle:" + heading + "|" + hash
}

// PrincipleHash — отпечаток абзаца у записи памяти: хранится среди её
// источников строкой «hash:…», чтобы не заводить колонку ради одного
// вида записей.
func PrincipleHash(m *store.Memory) string {
	for _, s := range m.Sources {
		if strings.HasPrefix(s, "hash:") {
			return strings.TrimPrefix(s, "hash:")
		}
	}
	return ""
}

// PrincipleBlocked — человек снял эту привязку и просил не заводить снова.
func (e *Engine) PrincipleBlocked(heading, hash string) bool {
	return e.store.OutsideState(principleKey(heading, hash)) == "done"
}

// BlockPrinciple — запомнить или забыть решение «не заводить снова».
func (e *Engine) BlockPrinciple(heading, hash string, block bool) error {
	key := principleKey(heading, hash)
	if !block {
		return e.store.ForgetOutside(key)
	}
	if _, err := e.store.OutsideStarted(key); err != nil {
		return err
	}
	return e.store.OutsideDone(key)
}

// AcceptPrinciples — принять пачку принципов от жителя машины. Пачка —
// весь файл целиком: только так видно, какой абзац исчез.
func (e *Engine) AcceptPrinciples(ctx context.Context, agent, path string, raw json.RawMessage) (string, error) {
	var in struct {
		Repo  string          `json:"repo"`
		Items []PrincipleItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("пачка принципов не разобрана: %w", err)
	}
	o := e.ExternalOrigin(agent, path)
	if o.Consumer == nil || !o.Consumer.Has(principleArea) {
		return "", errMemoryDenied(principleArea)
	}
	// что уже есть: заголовок → запись с её отпечатком и местами
	type known struct {
		mem    *store.Memory
		places []store.Place
	}
	have := map[string]*known{}
	for _, pl := range e.store.PlacesBySource(PrincipleSource) {
		heading := strings.TrimPrefix(pl.Source, PrincipleSource)
		k := have[heading]
		if k == nil {
			m, err := e.store.MemoryByID(pl.MemoryID)
			if err != nil {
				continue
			}
			k = &known{mem: m}
			have[heading] = k
		}
		k.places = append(k.places, pl)
	}
	// что прислали: заголовок → отпечаток, строка и места
	type batch struct {
		hash, text string
		places     []store.Place
	}
	got := map[string]*batch{}
	var order []string
	for _, it := range in.Items {
		it.Heading = strings.TrimSpace(it.Heading)
		if it.Heading == "" || it.Hash == "" || (it.Dir == "" && it.File == "") {
			continue
		}
		b := got[it.Heading]
		if b == nil {
			b = &batch{hash: it.Hash, text: strings.TrimSpace(it.Text)}
			got[it.Heading] = b
			order = append(order, it.Heading)
		}
		repo := it.Repo
		if repo == "" {
			repo = in.Repo
		}
		b.places = append(b.places, store.Place{Repo: repo, Dir: it.Dir, File: it.File,
			Source: PrincipleSource + it.Heading})
	}
	var added, updated, same, skipped, gone int
	logIt := func(action, detail string) {
		e.store.LogMemory(store.MemoryLogEntry{Consumer: o.Consumer.Label(), Area: principleArea,
			Action: action, Detail: detail, Granted: true})
	}
	for _, heading := range order {
		b := got[heading]
		if e.PrincipleBlocked(heading, b.hash) {
			skipped++
			continue
		}
		k := have[heading]
		if k != nil && PrincipleHash(k.mem) == b.hash {
			// тот же абзац: привязки приводятся к присланным — путь мог
			// разрешиться иначе (другой репозиторий, другой корень), и
			// прежняя координата тогда лишняя; заголовок вернулся после
			// исчезновения — снимаем «устарело»
			want := map[string]bool{}
			for _, pl := range b.places {
				pl.MemoryID = k.mem.ID
				want[placeKey(pl)] = true
				_ = e.store.BindToPlace(pl)
			}
			for _, pl := range k.places {
				if !want[placeKey(pl)] {
					_ = e.store.UnbindPlace(pl)
				}
			}
			for _, pl := range k.places {
				if pl.Doubtful {
					_ = e.store.MarkPlacesDoubtful(k.mem.ID, false)
					break
				}
			}
			// абзац тот же, а строка итога другая — значит, изменился не
			// текст, а наш разбор: запись правится на месте, без версии
			if text := clip(b.text, e.MemorySetting(memMaxChars)); text != "" && text != k.mem.Text {
				k.mem.Text = text
				_ = e.store.UpdateMemory(k.mem)
			}
			same++
			continue
		}
		if b.text == "" {
			continue
		}
		m := &store.Memory{Kind: store.MemFact, Area: principleArea, Text: clip(b.text, e.MemorySetting(memMaxChars)),
			Status: store.MemOK, Agent: "CLAUDE.md", Confirmed: true, Weight: 1,
			Sources: []string{"CLAUDE.md", "hash:" + b.hash}}
		if k != nil {
			m.Supersedes = k.mem.ID
		}
		id, err := e.store.SaveMemory(m)
		if err != nil {
			return "", err
		}
		for _, pl := range b.places {
			pl.MemoryID = id
			_ = e.store.BindToPlace(pl)
		}
		e.embedMemory(ctx, m)
		if k != nil {
			updated++
			logIt("remember", "принцип «"+heading+"» переписан: новая версия "+strconv.FormatInt(id, 10)+" вместо "+strconv.FormatInt(k.mem.ID, 10))
		} else {
			added++
			logIt("remember", "принцип «"+heading+"» заведён: запись "+strconv.FormatInt(id, 10)+", мест "+strconv.Itoa(len(b.places)))
		}
	}
	// абзацы, которых в файле больше нет: устарели, но не удалены
	var vanished []string
	for heading, k := range have {
		if _, ok := got[heading]; ok {
			continue
		}
		vanished = append(vanished, heading)
		fresh := false
		for _, pl := range k.places {
			if !pl.Doubtful {
				fresh = true
			}
		}
		if fresh {
			_ = e.store.MarkPlacesDoubtful(k.mem.ID, true)
			gone++
			logIt("observe", "принцип «"+heading+"» исчез из CLAUDE.md — привязки помечены устаревшими")
		}
	}
	sort.Strings(vanished)
	if added+updated+gone > 0 {
		log.Printf("память: принципы из CLAUDE.md от «%s»: заведено %d, переписано %d, без изменений %d, устарело %d, снято человеком %d",
			o.Consumer.Label(), added, updated, same, gone, skipped)
	}
	return fmt.Sprintf("Принципы приняты: заведено %d, переписано %d, без изменений %d, устарело %d, снято человеком %d.",
		added, updated, same, gone, skipped), nil
}

// placeKey — координата без записи: по ней сравниваются присланные и
// хранимые привязки одного абзаца.
func placeKey(p store.Place) string {
	return p.Repo + "|" + p.Dir + "|" + p.File + "|" + p.Symbol
}
