package store

// Перевод письма. Хранится отдельно от тела, а не поверх него: подлинник
// письма — это то, что прислали, и он не должен меняться оттого, что Сёма
// сходил к модели. Перевод помечен языком: человек сменил целевой язык —
// прежние переводы не подходят и просто не показываются, а сами записи
// остаются (второй раз к модели за тем же текстом ходить незачем).

import "time"

// Translation — перевод одного письма на один язык.
type Translation struct {
	MessageID int64
	Lang      string // на какой язык («Русский»)
	FromLang  string // с какого, как назвала модель; пустое — не определила
	Subject   string
	Text      string
	HTML      string // письмо целиком, с переведёнными словами и прежней вёрсткой
	MadeAt    string
}

func (s *Store) migrateTranslations() {
	_, _ = s.db.Exec(`ALTER TABLE message_translations ADD COLUMN html TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS message_translations (
		message_id INTEGER NOT NULL REFERENCES messages(id),
		lang       TEXT NOT NULL DEFAULT '',
		from_lang  TEXT NOT NULL DEFAULT '',
		subject    TEXT NOT NULL DEFAULT '',
		body       TEXT NOT NULL DEFAULT '',
		html       TEXT NOT NULL DEFAULT '',
		made_at    TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (message_id, lang)
	)`)
}

// SaveTranslation кладёт перевод. Повтор того же письма на тот же язык
// переписывает: модель могла ответить лучше, а двух переводов одного
// письма на один язык быть не должно.
func (s *Store) SaveTranslation(t Translation) error {
	_, err := s.db.Exec(`
INSERT INTO message_translations (message_id, lang, from_lang, subject, body, html, made_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(message_id, lang) DO UPDATE SET
  from_lang = excluded.from_lang, subject = excluded.subject,
  body = excluded.body, html = excluded.html, made_at = excluded.made_at`,
		t.MessageID, t.Lang, t.FromLang, t.Subject, t.Text, t.HTML, fmtTime(time.Now()))
	return err
}

// Translation — перевод письма на указанный язык. Нет — значит нет: пустой
// перевод и отсутствие перевода для экрана одно и то же.
func (s *Store) Translation(msgID int64, lang string) (Translation, bool) {
	t := Translation{MessageID: msgID, Lang: lang}
	err := s.db.QueryRow(`SELECT from_lang, subject, body, html, made_at FROM message_translations
WHERE message_id = ? AND lang = ?`, msgID, lang).Scan(&t.FromLang, &t.Subject, &t.Text, &t.HTML, &t.MadeAt)
	if err != nil || (t.Text == "" && t.HTML == "") {
		return Translation{}, false
	}
	return t, true
}

// TranslationStats — сколько писем переведено на этот язык. Число видно на
// экране настроек: без него человек не знает, работает ли перевод вообще.
func (s *Store) TranslationStats(lang string) int {
	n := 0
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM message_translations WHERE lang = ? AND body <> ''`, lang).Scan(&n)
	return n
}

// MessagesToTranslate — письма без перевода на этот язык, свежие первыми.
// Только те, у которых уже есть тело: переводить нечего, пока письмо не
// скачано целиком.
func (s *Store) MessagesToTranslate(lang string, limit int) ([]int64, error) {
	rows, err := s.db.Query(`
SELECT b.message_id FROM message_bodies b
JOIN messages m ON m.id = b.message_id
WHERE b.text <> ''
  AND NOT EXISTS (SELECT 1 FROM message_translations t
                  WHERE t.message_id = b.message_id AND t.lang = ?)
ORDER BY m.date DESC LIMIT ?`, lang, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
