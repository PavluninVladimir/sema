package store

// Новости по темам сняты 13 сентября 2026 по решению владельца: задача
// «Новости по темам», справочник тем и виденные ссылки, уведомления
// сводок. Миграция убирает их следы из живой базы; сама возможность поиска
// в интернете (роль «Поиск», страницы из ответа) остаётся.
func (s *Store) migrateNewsGone() error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS search_seen`,
		`DROP TABLE IF EXISTS search_topics`,
		`DELETE FROM tasks WHERE system = 'news'`,
		`DELETE FROM notifications WHERE key = 'news:daily'`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	// настройки — через дверь, чтобы правка легла в журнал изменений
	for _, k := range []string{"news.seed", "news.keep_days"} {
		s.DeleteSettingKey(k)
	}
	return nil
}
