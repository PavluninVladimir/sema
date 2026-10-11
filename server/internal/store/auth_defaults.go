package store

import "strings"

// Разовая миграция умолчаний входа (open-source, решение 5).
//
// Умолчания `auth.local_open` и `auth.local_scope` сузились: новая установка
// без проверки личности не пускает никого, а «этой машиной» считает только
// петлю — частная сеть становится своей лишь по выбору человека. Но правило
// проекта «сужение доступа — настройка со старым
// значением по умолчанию» держится для тех, кто уже работает: Сёма, который
// до обновления пускал свою сеть без входа, после обновления пускает так же
// и не запирает хозяина дома.
//
// Поэтому при открытии базы: ключ не записан, а данные в базе уже есть
// (ящики, письма, ключи устройств, сессии, токены) — записывается прежнее
// значение (`true`, `private`). Данных нет — записываются новые умолчания.
// Запись в обоих случаях явная, и в этом весь смысл: следующее открытие видит
// ключи записанными и ничего не решает заново. Иначе свежая установка, которой
// человек завёл ящик, при следующем старте сочла бы себя «обновлённой» и
// открылась бы своей сети сама.
//
// Записанное значение не трогается никогда — ни человеком, ни прошлой
// миграцией: у живого Сёмы владельца `auth.local_open = false` так и остаётся.
// Ключи решаются по одному: записан один — второй получает своё значение.

// settleAuthDefaults — записать умолчания входа, которых в базе ещё нет.
// Возвращает, что записано (ключ → значение), — для журнала и тестов.
func (s *Store) settleAuthDefaults() map[string]string {
	_, openSet := s.settingRow(AuthLocalOpen.Key)
	_, scopeSet := s.settingRow(AuthLocalScope.Key)
	if openSet && scopeSet {
		return nil
	}
	open, scope := "false", ScopeLoopback
	if s.hasOwnerData() {
		open, scope = "true", ScopePrivate
	}
	wrote := map[string]string{}
	if !openSet {
		if s.SetSetting(AuthLocalOpen.Key, open) == nil {
			wrote[AuthLocalOpen.Key] = open
		}
	}
	if !scopeSet {
		if s.SetSetting(AuthLocalScope.Key, scope) == nil {
			wrote[AuthLocalScope.Key] = scope
		}
	}
	return wrote
}

// hasOwnerData — в базе уже работали: заведён ящик, лежат письма, есть ключ
// устройства, сессия или токен. Справочники, которые Сёма засевает сама
// (провайдеры, скиллы, задачи), признаком не служат: они есть и у свежей
// установки.
func (s *Store) hasOwnerData() bool {
	if raw, ok := s.settingRow(accountsKey); ok {
		// пустой список ящиков json.Marshal пишет «[]», а не пустую строку
		if v := strings.TrimSpace(raw); v != "" && v != "[]" && v != "null" {
			return true
		}
	}
	for _, table := range []string{"messages", "passkeys", "sessions", "api_tokens"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// authDefaultsLine — записанное строкой для журнала: «auth.local_open = true».
func authDefaultsLine(wrote map[string]string) string {
	var parts []string
	for _, key := range []string{AuthLocalOpen.Key, AuthLocalScope.Key} {
		if v, ok := wrote[key]; ok {
			parts = append(parts, key+" = "+v)
		}
	}
	return strings.Join(parts, ", ")
}
