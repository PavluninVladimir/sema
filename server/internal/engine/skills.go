package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine/builtin"
	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Скиллы — инструкции Сёме: что проверять, как читать вывод, как
// отчитываться. Живут в базе (store.Skill), встроенные приезжают из
// builtin/skills/*.md, подмешиваются модели по словам-приметам и отдаются
// модулям по имени через sema.skill(name).

const (
	skillsMaxTokensKey = "skills.max_tokens"
	skillsMaxDefault   = 3000
)

func (e *Engine) SkillsMaxTokens() int { return e.store.SettingInt(skillsMaxTokensDef) }

// SkillScreen — адрес экрана скилла: `settings:skills@<имя>`. У каждого скилла
// свой экран, и уведомление про него ведёт туда, а не в общий список. Одним
// помощником, как ServerScreen: склейка адреса руками где-то ещё — это адрес,
// который забудут обновить.
func SkillScreen(name string) string {
	return "settings:skills@" + name
}

// parseSkillFile разбирает markdown с шапкой между строками «---».
func parseSkillFile(name, raw string) (*store.Skill, error) {
	k := &store.Skill{Name: name, Enabled: true, Source: store.SkillBuiltin}
	body := raw
	if strings.HasPrefix(raw, "---\n") {
		end := strings.Index(raw[4:], "\n---")
		if end < 0 {
			return nil, fmt.Errorf("шапка не закрыта")
		}
		head := raw[4 : 4+end]
		body = strings.TrimLeft(raw[4+end+4:], "\n")
		for _, line := range strings.Split(head, "\n") {
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			switch strings.TrimSpace(key) {
			case "title":
				k.Title = val
			case "group":
				k.Group = val
			case "keywords":
				k.Keywords = val
			case "when":
				k.When = val
			}
		}
	}
	k.Text = strings.TrimSpace(body)
	if k.Title == "" {
		k.Title = name
	}
	if k.Text == "" {
		return nil, fmt.Errorf("пустой текст")
	}
	return k, nil
}

func skillHash(k *store.Skill) string {
	h := sha256.Sum256([]byte(k.Title + "\n" + k.Group + "\n" + k.Keywords + "\n" + k.When + "\n" + k.Text))
	return hex.EncodeToString(h[:8])
}

// loadBuiltinSkills — скиллы из сборки: новые включёнными, изменившиеся —
// с новым текстом, если человек их не правил (EditedBy пуст).
func (e *Engine) loadBuiltinSkills() {
	entries, err := fs.ReadDir(builtin.FS, "skills")
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		raw, err := fs.ReadFile(builtin.FS, "skills/"+ent.Name())
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(ent.Name(), ".md")
		fresh, err := parseSkillFile(name, string(raw))
		if err != nil {
			log.Printf("встроенный скилл %s: %v", name, err)
			continue
		}
		fresh.Hash = skillHash(fresh)
		old, exists := e.store.Skill(name)
		switch {
		case !exists:
			if err := e.store.SaveSkill(fresh); err != nil {
				log.Printf("скилл %s: %v", name, err)
			} else {
				log.Printf("скилл «%s» из сборки установлен", fresh.Title)
			}
		case old.Source == store.SkillBuiltin && old.EditedBy == "" && old.Hash != fresh.Hash:
			fresh.Enabled, fresh.Created, fresh.Uses, fresh.LastUsed = old.Enabled, old.Created, old.Uses, old.LastUsed
			fresh.EditedAt = time.Now().Format(time.RFC3339)
			if err := e.store.SaveSkill(fresh); err != nil {
				log.Printf("скилл %s: %v", name, err)
			} else {
				log.Printf("скилл «%s» обновлён из сборки", fresh.Title)
			}
		}
	}
}

// BuiltinSkillText — исходный текст встроенного скилла для «Вернуть встроенный».
func BuiltinSkillText(name string) (*store.Skill, bool) {
	raw, err := fs.ReadFile(builtin.FS, "skills/"+name+".md")
	if err != nil {
		return nil, false
	}
	k, err := parseSkillFile(name, string(raw))
	if err != nil {
		return nil, false
	}
	k.Hash = skillHash(k)
	return k, true
}

func (e *Engine) Skills() []*store.Skill {
	list, _ := e.store.Skills()
	return list
}

// SkillGroups — блоки вкладки в порядке появления.
func (e *Engine) SkillGroups() []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range e.Skills() {
		g := k.Group
		if g == "" {
			g = "Прочее"
		}
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

var reSkillName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

// SaveSkill — правка с экрана или из чата: секреты в текст не попадают.
func (e *Engine) SaveSkill(k *store.Skill, who string) error {
	if !reSkillName.MatchString(k.Name) {
		return fmt.Errorf("имя скилла — латиница, цифры и дефис: linux-admin")
	}
	if k.Name == "new" {
		// имя едет в адресе экрана скилла, и «new» там — экран «Добавить скилл»
		return fmt.Errorf("имя «new» занято: так в адресе зовётся экран добавления скилла")
	}
	var oldText string
	if old, ok := e.store.Skill(k.Name); ok {
		oldText = old.Text
	}
	if err := e.checkSkillText(k.Text, oldText); err != nil {
		return err
	}
	if old, ok := e.store.Skill(k.Name); ok {
		k.Created, k.Uses, k.LastUsed = old.Created, old.Uses, old.LastUsed
		if k.Source == "" {
			k.Source = old.Source
		}
		if k.Hash == "" {
			k.Hash = old.Hash
		}
	}
	k.EditedBy, k.EditedAt = who, time.Now().Format(time.RFC3339)
	return e.store.SaveSkill(k)
}

// ResetSkill возвращает встроенный текст: правки человека забываются.
func (e *Engine) ResetSkill(name string) error {
	fresh, ok := BuiltinSkillText(name)
	if !ok {
		return fmt.Errorf("у скилла %q нет встроенного текста", name)
	}
	if old, ok := e.store.Skill(name); ok {
		fresh.Enabled, fresh.Created, fresh.Uses, fresh.LastUsed = old.Enabled, old.Created, old.Uses, old.LastUsed
	}
	return e.store.SaveSkill(fresh)
}

var reSecretLike = regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(sk|ghp|gho|xox[bp]|sema)_[A-Za-z0-9]{16,}|AKIA[0-9A-Z]{16}`)

// checkSkillText проверяет только строки, которых не было в прежнем тексте
// скилла: то, что уже лежит в базе, человек либо принял, либо получил из
// сборки, и ронять каждую правку из-за строки, которой он не касался,
// значит не давать править скилл вовсе (так встало со встроенным
// «Linux-сервер»). Новому скиллу прежнего текста нет — проверяется весь.
func (e *Engine) checkSkillText(text, oldText string) error {
	known := map[string]bool{}
	for _, line := range strings.Split(oldText, "\n") {
		known[strings.TrimSpace(line)] = true
	}
	for i, line := range strings.Split(text, "\n") {
		if oldText != "" && known[strings.TrimSpace(line)] {
			continue
		}
		if reSecretLike.MatchString(line) {
			return fmt.Errorf("строка %d похожа на приватный ключ или токен — секреты живут в хранилище, а не в скилле", i+1)
		}
		if e.secretMask(line) != "" {
			return fmt.Errorf("строка %d похожа на секрет по маскам из «Памяти» — уберите её", i+1)
		}
	}
	return nil
}

// matchSkills — сколько примет скилла встретилось в тексте; для блока
// «Администрирование» приметой считаются и имена серверов.
func (e *Engine) matchSkills(userText string) map[string]int {
	low := strings.ToLower(userText)
	hosts := e.HostNames()
	out := map[string]int{}
	for _, k := range e.Skills() {
		if !k.Enabled {
			continue
		}
		n := 0
		for _, w := range strings.Split(k.Keywords, ",") {
			w = strings.ToLower(strings.TrimSpace(w))
			if w != "" && strings.Contains(low, w) {
				n++
			}
		}
		if strings.EqualFold(k.Group, "Администрирование") {
			for _, h := range hosts {
				if strings.Contains(low, strings.ToLower(h)) {
					n++
				}
			}
		}
		if n > 0 {
			out[k.Name] = n
		}
	}
	return out
}

// skillsPrompt — опись включённых скиллов всегда, полный текст — по приметам
// в пределах бюджета токенов. Возвращает текст и имена подмешанных.
func (e *Engine) skillsPrompt(userText string) (string, []string) {
	all := e.Skills()
	var enabled []*store.Skill
	for _, k := range all {
		if k.Enabled {
			enabled = append(enabled, k)
		}
	}
	if len(enabled) == 0 {
		return "", nil
	}
	matches := e.matchSkills(userText)
	sort.SliceStable(enabled, func(i, j int) bool { return matches[enabled[i].Name] > matches[enabled[j].Name] })
	var b strings.Builder
	b.WriteString("\nSkills — Sema's own instructions (Russian). Follow a skill whose text is given below; the list shows the rest by name:\n")
	budget := e.SkillsMaxTokens()
	var used []string
	for _, k := range enabled {
		when := k.When
		if when == "" {
			when = clip(strings.ReplaceAll(k.Text, "\n", " "), 120)
		}
		if matches[k.Name] > 0 && tokensOf(k.Text) <= budget {
			budget -= tokensOf(k.Text)
			used = append(used, k.Name)
			b.WriteString("\n### Skill «" + k.Title + "» (name: " + k.Name + ")\n" + k.Text + "\n")
			continue
		}
		b.WriteString("- " + k.Title + " (name: " + k.Name + "): " + when + "\n")
	}
	b.WriteString("To change a skill on request, answer with a block ```sema-skill:<name>\\n<full new text>``` " +
		"(for a new skill start the text with a header: title:, group:, keywords:, when: lines, then a line with three dashes); " +
		"Sema shows the difference and applies it only after the user accepts.\n")
	for _, n := range used {
		e.store.SkillUsed(n)
	}
	return b.String(), used
}

// skillTools — sema.skill(name) для модуля: только перечисленные в манифесте
// и включённые; про остальные — пустая строка и запись в журнал модуля.
func (e *Engine) skillTools(m *Module) []script.Tool {
	return []script.Tool{{
		Name: "skill", Need: "skills", Args: "(name)",
		Desc: "текст скилла из манифеста модуля — правила, которые модуль отдаёт модели вместе с данными",
		Fn: func(a []any) (any, error) {
			name := script.Str(a, 0)
			for _, s := range m.Skills {
				if s == name {
					if k, ok := e.store.Skill(name); ok && k.Enabled {
						e.store.SkillUsed(name)
						return k.Text, nil
					}
					return "", nil
				}
			}
			log.Printf("модуль %s просил скилл %q, которого нет в манифесте", m.Name, name)
			return "", nil
		},
	}}
}

// ---- предложение скилла из чата ----

var skillBlock = regexp.MustCompile("(?s)```sema-skill:([^\n]*)\n(.*?)```")

// SkillCard — карточка предложения в переписке: что меняется, принято ли.
type SkillCard struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	New    bool   `json:"new"`
	Diff   string `json:"diff"`
	Status string `json:"status"` // proposed | accepted | rejected | stale
	ID     string `json:"id"`
}

// findSkillBlocks вынимает предложения скиллов из ответа модели и кладёт
// карточки; черновик ждёт решения человека в таблице skill_drafts.
func (e *Engine) findSkillBlocks(chatID int64, answer string) string {
	for _, m := range skillBlock.FindAllStringSubmatch(answer, 3) {
		name := strings.TrimSpace(m[1])
		body := strings.TrimSpace(m[2])
		if name == "" || body == "" {
			continue
		}
		proposed, err := parseSkillFile(name, body+"\n")
		if err != nil {
			_ = e.saveToolCard(chatID, ToolCard{Title: "Скилл «" + name + "»", Err: "не разобран: " + err.Error()})
			continue
		}
		old, exists := e.store.Skill(name)
		if exists {
			if proposed.Title == name {
				proposed.Title = old.Title
			}
			if proposed.Group == "" {
				proposed.Group = old.Group
			}
			if proposed.Keywords == "" {
				proposed.Keywords = old.Keywords
			}
			if proposed.When == "" {
				proposed.When = old.When
			}
			proposed.Source, proposed.Hash, proposed.Enabled = old.Source, old.Hash, old.Enabled
		} else {
			proposed.Source = store.SkillChat
			if proposed.Group == "" {
				proposed.Group = "Прочее"
			}
		}
		title := "чат"
		if c, err := e.store.Chat(chatID); err == nil && c.Title != "" {
			title = "чат «" + c.Title + "»"
		}
		diff := ""
		if exists {
			diff = lineDiff(old.Text, proposed.Text)
		} else {
			diff = clip(proposed.Text, 600)
		}
		// черновик живёт в базе: предложение, пропавшее после перезапуска,
		// — потерянная работа
		d := &store.SkillDraft{
			ID: fmt.Sprintf("%d-%d", chatID, time.Now().UnixNano()), Name: proposed.Name, Title: proposed.Title,
			Group: proposed.Group, Keywords: proposed.Keywords, When: proposed.When, Text: proposed.Text,
			IsNew: !exists, Kind: store.DraftChat, Who: title, Why: "предложено в переписке", Diff: diff, Chat: chatID,
		}
		if err := e.store.AddSkillDraft(d); err != nil {
			_ = e.saveToolCard(chatID, ToolCard{Title: "Скилл «" + name + "»", Err: "черновик не сохранён: " + err.Error()})
			continue
		}
		card := ToolCard{
			Title:  "Скилл «" + proposed.Title + "»",
			Result: "предложение: примите — и скилл изменится",
			Skill:  &SkillCard{Name: name, Title: proposed.Title, New: !exists, Diff: diff, Status: "proposed", ID: d.ID},
		}
		_ = e.saveToolCard(chatID, card)
		e.notifySkillProposal(d.ID, proposed, !exists, diff, title)
	}
	return strings.TrimSpace(skillBlock.ReplaceAllString(answer, ""))
}

// DecideSkill — «Принять» или «Отклонить» на карточке. Решение идёт по
// черновику из базы: перезапуск Сёмы предложение больше не теряет.
func (e *Engine) DecideSkill(id string, accept bool) error {
	d, ok := e.store.SkillDraft(id)
	if !ok {
		return fmt.Errorf("предложение не найдено — попросите ещё раз")
	}
	return e.DecideDraft(id, accept, d.Who)
}

// lineDiff — что добавилось и что убрано, по строкам; без библиотек, для
// карточки достаточно.
func lineDiff(oldText, newText string) string {
	oldSet := map[string]bool{}
	for _, l := range strings.Split(oldText, "\n") {
		oldSet[strings.TrimSpace(l)] = true
	}
	newSet := map[string]bool{}
	for _, l := range strings.Split(newText, "\n") {
		newSet[strings.TrimSpace(l)] = true
	}
	var b strings.Builder
	added, removed := 0, 0
	for _, l := range strings.Split(newText, "\n") {
		t := strings.TrimSpace(l)
		if t != "" && !oldSet[t] {
			added++
			if added <= 20 {
				b.WriteString("+ " + t + "\n")
			}
		}
	}
	for _, l := range strings.Split(oldText, "\n") {
		t := strings.TrimSpace(l)
		if t != "" && !newSet[t] {
			removed++
			if removed <= 20 {
				b.WriteString("− " + t + "\n")
			}
		}
	}
	if added == 0 && removed == 0 {
		return "текст не изменился"
	}
	return fmt.Sprintf("+%d строк, −%d строк\n", added, removed) + b.String()
}
