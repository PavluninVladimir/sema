package auth

// Умолчания настроек входа — в реестре `store.SettingDef`, а не числом
// рядом с чтением: экран «Доступа» показывает «по умолчанию» по этим же
// описаниям (см. store/setting_defs.go и store/setting_defs_auth.go).
// Поведение входа от переезда умолчаний не меняется — кроме двух умолчаний,
// суженных нарочно (`auth.local_open`, `auth.local_scope`; open-source,
// решение 5): работающему Сёме прежние значения записывает разовая миграция
// (store/auth_defaults.go).

import "github.com/PavluninVladimir/sema/server/internal/store"

var (
	phraseLimitDef = store.AuthPhraseLimit
	rpIDDef        = store.AuthRPID
	originDef      = store.AuthOrigin
	localHostsDef  = store.AuthLocalHosts
	localOpenDef   = store.AuthLocalOpen
	localScopeDef  = store.AuthLocalScope
)
