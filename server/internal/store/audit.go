package store

import (
	"fmt"
	"strings"
	"time"
)

// Журнал отправленного: что именно Сёма отдал модели. Расход показывает
// цену, журнал — содержимое. Секреты в него не попадают: текст проходит
// маски до записи.

type SentRow struct {
	ID       int64
	At       string
	Consumer string
	Model    string
	Outside  bool // ушло за пределы своей сети
	// Untrusted — модель, которой человек не доверил закрытое. Это другой
	// вопрос, чем Outside: доверенная модель бывает удалённой (законно даёт
	// outside = 1), а недоверенная — стоять в соседнем контейнере. Проверять
	// «секретное не утекло» надо по этому полю, а не по адресу.
	Untrusted bool
	Kind      string // text | stream | image | search | transcribe
	Class     string // обычное | чувствительное | секретное
	TokensIn  int
	TokensOut int
	Seconds   float64
	OK        bool
	Retry     bool   // неудачу переделают: это повтор, а не отказ
	Sources   string // что было в контексте: письма, файлы, записи памяти
	Preview   string // выжимка отправленного
	Prompt    string // полный текст после масок
	Answer    string // ответ модели после масок
	Err       string
}

const (
	ClassPlain     = "обычное"
	ClassSensitive = "чувствительное"
	ClassSecret    = "секретное"
)

func (s *Store) migrateAudit() error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS sent_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			at         TEXT NOT NULL,
			consumer   TEXT NOT NULL DEFAULT '',
			model      TEXT NOT NULL DEFAULT '',
			outside    INTEGER NOT NULL DEFAULT 0,
			kind       TEXT NOT NULL DEFAULT 'text',
			class      TEXT NOT NULL DEFAULT 'обычное',
			tokens_in  INTEGER NOT NULL DEFAULT 0,
			tokens_out INTEGER NOT NULL DEFAULT 0,
			seconds    REAL NOT NULL DEFAULT 0,
			ok         INTEGER NOT NULL DEFAULT 1,
			retry      INTEGER NOT NULL DEFAULT 0,
			sources    TEXT NOT NULL DEFAULT '',
			preview    TEXT NOT NULL DEFAULT '',
			prompt     TEXT NOT NULL DEFAULT '',
			answer     TEXT NOT NULL DEFAULT '',
			err        TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sent_log_at ON sent_log(at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_sent_log_outside ON sent_log(outside, at DESC)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("журнал отправленного: %w", err)
		}
	}
	// недоверие появилось позже адреса: у старых строк его нет, и ноль
	// здесь значит «не записывали», а не «модель была доверенной»
	_, _ = s.db.Exec(`ALTER TABLE sent_log ADD COLUMN untrusted INTEGER NOT NULL DEFAULT 0`)
	return nil
}

const sentCols = `id, at, consumer, model, outside, untrusted, kind, class, tokens_in, tokens_out, seconds, ok, retry, sources, preview, prompt, answer, err`

func scanSent(r rowScanner) (*SentRow, error) {
	s := &SentRow{}
	var outside, untrusted, ok, retry int
	err := r.Scan(&s.ID, &s.At, &s.Consumer, &s.Model, &outside, &untrusted, &s.Kind, &s.Class, &s.TokensIn, &s.TokensOut,
		&s.Seconds, &ok, &retry, &s.Sources, &s.Preview, &s.Prompt, &s.Answer, &s.Err)
	s.Outside, s.Untrusted, s.OK, s.Retry = outside != 0, untrusted != 0, ok != 0, retry != 0
	return s, err
}

func (s *Store) AddSent(row *SentRow) error {
	if row.At == "" {
		row.At = time.Now().Format(time.RFC3339)
	}
	if row.Class == "" {
		row.Class = ClassPlain
	}
	_, err := s.db.Exec(`INSERT INTO sent_log (at, consumer, model, outside, untrusted, kind, class, tokens_in, tokens_out, seconds, ok, retry, sources, preview, prompt, answer, err)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.At, row.Consumer, row.Model, b2i(row.Outside), b2i(row.Untrusted), row.Kind, row.Class, row.TokensIn, row.TokensOut,
		row.Seconds, b2i(row.OK), b2i(row.Retry), row.Sources, row.Preview, row.Prompt, row.Answer, row.Err)
	return err
}

// CountSentSince — сколько записей такого вида этот потребитель оставил в
// журнале с указанного времени: так считаются пределы, которые должны
// пережить перезапуск (`web.find_per_day`).
func (s *Store) CountSentSince(consumer, kind string, since time.Time) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sent_log WHERE consumer = ? AND kind = ? AND at >= ?`,
		consumer, kind, since.Format(time.RFC3339)).Scan(&n)
	return n
}

// SentList — отправки с фильтрами: только наружу, только класс, поиск по
// выжимке и источникам.
func (s *Store) SentList(outsideOnly bool, class, search string, limit int) ([]*SentRow, error) {
	q := `SELECT ` + sentCols + ` FROM sent_log WHERE 1 = 1`
	var args []any
	if outsideOnly {
		q += ` AND outside = 1`
	}
	if class != "" {
		q += ` AND class = ?`
		args = append(args, class)
	}
	if search != "" {
		q += ` AND (preview LIKE ? OR sources LIKE ? OR consumer LIKE ? OR model LIKE ?)`
		like := "%" + search + "%"
		args = append(args, like, like, like, like)
	}
	if limit <= 0 {
		limit = 100
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SentRow
	for rows.Next() {
		r, err := scanSent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Sent(id int64) (*SentRow, bool) {
	r, err := scanSent(s.db.QueryRow(`SELECT `+sentCols+` FROM sent_log WHERE id = ?`, id))
	return r, err == nil
}

// SentStat — сводка за период: сколько ушло наружу и сколько из этого было
// чувствительным, плюс повторы и отказы.
type SentStat struct {
	Calls     int
	Outside   int
	Sensitive int
	Retries   int
	Fails     int
	// SecretUntrusted — сколько раз секретное ушло модели, которой человек
	// не доверил закрытое. Это и есть число, которое обязано быть нулём.
	// Прежний дневной запрос считал «наружу и секретное» и потому врал в
	// обе стороны: доверенная удалённая модель попадала в счёт, а
	// недоверенная в своей сети — нет.
	SecretUntrusted int
	// TokensOut — сколько текста ушло наружу, то есть входных токенов
	// облачных вызовов: интересует объём отданного, а не полученного.
	// Имя историческое; переименование ломает экраны, поэтому пояснение.
	TokensOut int
}

func (s *Store) SentStats(since string) SentStat {
	var st SentStat
	row := s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(outside), 0),
		COALESCE(SUM(outside = 1 AND class <> ?), 0),
		COALESCE(SUM(ok = 0 AND retry = 1), 0),
		COALESCE(SUM(ok = 0 AND retry = 0), 0),
		COALESCE(SUM(CASE WHEN outside = 1 THEN tokens_in ELSE 0 END), 0), -- именно вход: столько текста ушло из дома
		COALESCE(SUM(untrusted = 1 AND class = ? AND ok = 1), 0)
		FROM sent_log WHERE at >= ?`, ClassPlain, ClassSecret, since)
	_ = row.Scan(&st.Calls, &st.Outside, &st.Sensitive, &st.Retries, &st.Fails, &st.TokensOut, &st.SecretUntrusted)
	return st
}

// SentClassCounts — сколько отправок каждого класса за период. Нужен
// счётчик у чипов «чувствительное» и «секретное»: без него человек не
// знает, есть ли там вообще что-то, и жмёт наугад. Считается одним
// проходом по индексу времени и не тянет ни промпт, ни ответ — они
// весят сотни килобайт на запись.
func (s *Store) SentClassCounts(since string) map[string]int {
	out := map[string]int{}
	rows, err := s.db.Query(`SELECT class, COUNT(*) FROM sent_log WHERE at >= ? GROUP BY class`, since)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var class string
		var n int
		if rows.Scan(&class, &n) == nil {
			out[class] = n
		}
	}
	return out
}

func (s *Store) PurgeSent(before string) int64 {
	res, err := s.db.Exec(`DELETE FROM sent_log WHERE at < ?`, before)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// SentSourcesLine — короткая строка источников для записи журнала.
func SentSourcesLine(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, " · ")
}
