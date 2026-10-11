package store

// Справочник поставщиков.
//
// В документах один и тот же поставщик пишется как придётся: на живой Сёме
// «АО "Горэнерго"», «АО Горэнерго» и «ОП Городское отделение
// АО Горэнерго» — двенадцать документов на 17 051 ₽, которые в любом
// списке стоят тремя разными строками и никогда не складываются. «Умный
// ритейл» — в двух написаниях, шестнадцать документов.
//
// Правило устройства одно: **сырое имя на файле не трогаем**. Оно так и
// написано в документе, и это факт. Справочник — это отдельная связь: у
// файла появляется ссылка на поставщика, а как он назван в бумаге, видно
// по-прежнему. Поэтому слияние и переименование ничего не портят и
// отменяются.

import (
	"strings"
	"time"
)

// Vendor — поставщик справочника.
type Vendor struct {
	ID    int64
	Name  string // каноническое имя: как человек его называет
	Note  string
	Files int     // сколько документов
	Sum   float64 // на сколько
	Names []string
}

func (s *Store) migrateVendors() {
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS vendors (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL UNIQUE,
		note       TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT ''
	)`)
	// как поставщик написан в документе → кто он на самом деле. Ключ —
	// нормализованное написание: «АО "Горэнерго"» и «ОП Городское
	// отделение АО Горэнерго» дают один ключ
	// ключ — пара «нормализованное написание + как написано в документе»:
	// у одного ключа написаний много, и все они должны остаться видны.
	// По одному только ключу мы бы помнили последнее и забывали остальные
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS vendor_aliases (
		alias   TEXT NOT NULL,
		raw     TEXT NOT NULL DEFAULT '',
		vendor  INTEGER NOT NULL,
		by      TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (alias, raw)
	)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_alias_vendor ON vendor_aliases(vendor)`)
	_, _ = s.db.Exec(`ALTER TABLE attachments ADD COLUMN vendor_id INTEGER NOT NULL DEFAULT 0`)
	// дата самого документа и срок оплаты: «когда счёт» и «когда платить» —
	// разные вопросы, и оба не совпадают с датой письма
	_, _ = s.db.Exec(`ALTER TABLE attachments ADD COLUMN doc_date TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE attachments ADD COLUMN due_date TEXT NOT NULL DEFAULT ''`)
	// счёт к оплате или подтверждение платежа. Без этого различия «чеками»
	// зовётся и то и другое, и вопрос «а этот счёт я оплатил?» задать нечем
	_, _ = s.db.Exec(`ALTER TABLE attachments ADD COLUMN to_pay INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_att_vendor ON attachments(vendor_id)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_att_due ON attachments(due_date)`)
	// конверт письма: на что оно отвечает, вся цепочка, кому адресовано и
	// как отписаться. До этого «есть ли наш ответ» проверялось совпадением
	// темы, то есть угадыванием
	for _, q := range []string{
		`ALTER TABLE messages ADD COLUMN in_reply_to TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN refs TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN reply_to TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN to_addrs TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN cc_addrs TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE messages ADD COLUMN list_unsub TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = s.db.Exec(q)
	}
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_inreply ON messages(in_reply_to)`)
}

// VendorByAlias — кто это, если такое написание уже встречалось.
func (s *Store) VendorByAlias(alias string) (int64, bool) {
	var id int64
	if err := s.db.QueryRow(`SELECT vendor FROM vendor_aliases WHERE alias = ? ORDER BY rowid LIMIT 1`, alias).Scan(&id); err != nil {
		return 0, false
	}
	return id, id > 0
}

// EnsureVendor — поставщик по каноническому имени; нет такого — заводим.
func (s *Store) EnsureVendor(name string) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM vendors WHERE name = ?`, name).Scan(&id); err == nil && id > 0 {
		return id, nil
	}
	res, err := s.db.Exec(`INSERT INTO vendors (name, created_at) VALUES (?, ?)`,
		name, time.Now().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LinkAlias — запомнить написание. Уже знакомую пару не переписываем: если
// человек слил поставщиков руками, следующий документ с тем же написанием
// не должен растащить их обратно. Решение человека сильнее нашего.
func (s *Store) LinkAlias(alias, raw string, vendor int64, by string) error {
	_, err := s.db.Exec(`INSERT INTO vendor_aliases (alias, raw, vendor, by) VALUES (?, ?, ?, ?)
ON CONFLICT(alias, raw) DO NOTHING`, alias, raw, vendor, by)
	return err
}

// SetFileVendor — привязать файл к поставщику. Сырое имя в `vendor`
// остаётся: так написано в документе.
func (s *Store) SetFileVendor(fileID, vendor int64) error {
	_, err := s.db.Exec(`UPDATE attachments SET vendor_id = ? WHERE id = ?`, vendor, fileID)
	return err
}

// SetFileDates — дата документа, срок оплаты и «это счёт, а не чек».
func (s *Store) SetFileDates(fileID int64, doc, due string, toPay bool) error {
	n := 0
	if toPay {
		n = 1
	}
	_, err := s.db.Exec(`UPDATE attachments SET doc_date = ?, due_date = ?, to_pay = ? WHERE id = ?`,
		doc, due, n, fileID)
	return err
}

// Vendors — справочник со счётчиками. Пустые (все документы отвязали)
// показываем тоже: человек их завёл, и молча пропадать они не должны.
func (s *Store) Vendors() ([]*Vendor, error) {
	rows, err := s.db.Query(`
SELECT v.id, v.name, v.note,
       COUNT(a.id), COALESCE(SUM(a.amount), 0)
FROM vendors v LEFT JOIN attachments a ON a.vendor_id = v.id
GROUP BY v.id ORDER BY COUNT(a.id) DESC, v.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Vendor
	byID := map[int64]*Vendor{}
	for rows.Next() {
		v := &Vendor{}
		if err := rows.Scan(&v.ID, &v.Name, &v.Note, &v.Files, &v.Sum); err != nil {
			return nil, err
		}
		out = append(out, v)
		byID[v.ID] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// написания: по ним видно, что именно слилось в одну строку
	arows, err := s.db.Query(`SELECT vendor, raw FROM vendor_aliases ORDER BY raw`)
	if err != nil {
		return out, nil
	}
	defer arows.Close()
	for arows.Next() {
		var id int64
		var raw string
		if arows.Scan(&id, &raw) == nil && raw != "" {
			if v := byID[id]; v != nil {
				v.Names = append(v.Names, raw)
			}
		}
	}
	return out, nil
}

// Vendor — один поставщик.
func (s *Store) VendorByID(id int64) (*Vendor, bool) {
	list, err := s.Vendors()
	if err != nil {
		return nil, false
	}
	for _, v := range list {
		if v.ID == id {
			return v, true
		}
	}
	return nil, false
}

// MergeVendors — слить второго в первого: написания и файлы переезжают,
// запись второго уходит. Сырые имена на файлах не трогаются.
func (s *Store) MergeVendors(keep, gone int64) error {
	if keep == gone || keep == 0 || gone == 0 {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE vendor_aliases SET vendor = ? WHERE vendor = ?`, keep, gone); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE attachments SET vendor_id = ? WHERE vendor_id = ?`, keep, gone); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM vendors WHERE id = ?`, gone)
	return err
}

// RenameVendor — как человек называет поставщика. На документы не влияет.
func (s *Store) RenameVendor(id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE vendors SET name = ? WHERE id = ?`, name, id)
	return err
}

// ReplyTo — есть ли среди наших отправленных писем ответ на это. Смотрим
// конверт: `In-Reply-To` — прямой ответ, `References` — ответ где-то в
// цепочке. Тему не спрашиваем вовсе: «Re: Счёт» бывает у десятка разных
// разговоров, и по ней Сёма отвечала бы «ответ найден» на чужой ответ.
func (s *Store) ReplyTo(messageID string) (bool, error) {
	id := strings.Trim(strings.TrimSpace(messageID), "<>")
	if id == "" {
		return false, nil
	}
	n := 0
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages
WHERE direction = 'out' AND (in_reply_to LIKE ? OR refs LIKE ?)`,
		"%"+id+"%", "%"+id+"%").Scan(&n)
	return n > 0, err
}

// BillsDue — счета к оплате со сроком начиная с указанного дня, ближайшие
// первыми. Только счета (`to_pay`), только с проставленным сроком.
func (s *Store) BillsDue(from string, limit int) ([]*Attachment, error) {
	return s.invoices(`a.to_pay = 1 AND a.due_date >= ? ORDER BY a.due_date ASC`, from, limit)
}

// BillsOverdue — счета, срок которых прошёл раньше рубежа.
func (s *Store) BillsOverdue(before string, limit int) ([]*Attachment, error) {
	return s.invoices(`a.to_pay = 1 AND a.due_date <> '' AND a.due_date < ? ORDER BY a.due_date DESC`, before, limit)
}

func (s *Store) invoices(where, arg string, limit int) ([]*Attachment, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(selectAtt+` WHERE `+where+` LIMIT ?`, arg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReceiptsAfter — документы об оплате от того же поставщика начиная с даты
// счёта. Поставщик ищется и по справочнику, и по сырому имени: у старых
// записей связи может ещё не быть.
func (s *Store) ReceiptsAfter(vendorID int64, vendorText, since string, limit int) ([]*Attachment, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(selectAtt+`
		WHERE a.kind = ? AND a.to_pay = 0
		  AND (a.vendor_id = ? AND ? > 0 OR a.vendor = ?)
		  AND COALESCE(NULLIF(a.doc_date, ''), NULLIF(m.date, ''), a.created_at) >= ?
		LIMIT ?`, AttKindReceipt, vendorID, vendorID, vendorText, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FilesWithVendorText — файлы, у которых имя поставщика написано в
// документе, но справочнику ещё не сопоставлено.
func (s *Store) FilesWithVendorText(limit int) ([]*Attachment, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(selectAtt+` WHERE a.vendor <> '' AND a.vendor_id = 0 ORDER BY a.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FilesOfVendor — документы поставщика, свежие первыми.
func (s *Store) FilesOfVendor(id int64, limit int) ([]*Attachment, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(selectAtt+` WHERE a.vendor_id = ?
		ORDER BY COALESCE(NULLIF(a.doc_date, ''), NULLIF(m.date, ''), a.created_at) DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attachment
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UndoRejudge — вернуть письма, которые тронул слишком широкий досуд.
// «Как было» — это архив без уверенности: именно так их положил обход
// папок.
//
// Причина пишется так, как её прочтёт человек в графе «Почему»: «из папки
// ящика, разбора не было». Прежняя формулировка — «папка: не запомнена
// (возврат после ошибочного разбора)» — была служебной запиской самому
// себе и стояла у каждого письма в сводке. В графе «Почему» должно быть
// объяснение решения, а не след нашей уборки.
func (s *Store) UndoRejudge() (int, error) {
	res, err := s.db.Exec(`UPDATE messages
SET status = ?, confidence = 0, reason = 'из папки ящика, разбора не было'
WHERE reason LIKE '%разобрано задним числом%'`, StatusArchived)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.touch(ScreenInbox)
	}
	return int(n), nil
}

// DropAllWork — снять из очереди ВСЕ незавершённые работы вида. Отличается
// от DropWork, которая снимает только отложенное человеком: здесь работы
// поставлены по ошибке нами, и оставить их значило бы гонять модель по
// списку, который мы сами признали неверным.
func (s *Store) DropAllWork(kind string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM work_queue WHERE kind = ? AND state <> 'done'`, kind)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CountFromSenderWithStatus — сколько писем отправителя лежит в этом
// состоянии; по нему Сёма учится у спама провайдера.
func (s *Store) CountFromSenderWithStatus(addr, status string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE status = ? AND lower(from_addr) = ?`,
		status, strings.ToLower(strings.TrimSpace(addr))).Scan(&n)
	return n, err
}

// MessagesFromSender — письма отправителя, свежие сначала: по ним видно,
// не подтвердил ли он оплату письмом.
func (s *Store) MessagesFromSender(addr string, limit int) ([]*Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(selectMsg+` WHERE lower(from_addr) = ? ORDER BY date DESC LIMIT ?`,
		strings.ToLower(strings.TrimSpace(addr)), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PendingFromSender — письма отправителя, ждущие решения человека.
// Отдельный запрос, а не отбор из показанного: на экране видна страница
// списка, а решение «все письма этого отправителя» касается всех.
func (s *Store) PendingFromSender(addr string, limit int) ([]*Message, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(selectMsg+` WHERE status = ? AND lower(from_addr) = ?
ORDER BY date DESC LIMIT ?`, StatusPending, strings.ToLower(strings.TrimSpace(addr)), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanOne(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// FixRepairNote — переписать служебную записку, попавшую в графу «Почему».
// «папка: не запомнена (возврат после ошибочного разбора)» стояло у
// каждого из тридцати писем в сводке: в этой графе человек ждёт
// объяснения решения, а не следа нашей уборки.
func (s *Store) FixRepairNote() (int, error) {
	res, err := s.db.Exec(`UPDATE messages SET reason = 'из папки ящика, разбора не было'
WHERE reason LIKE '%возврат после ошибочного разбора%'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.touch(ScreenInbox)
	}
	return int(n), nil
}
