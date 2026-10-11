package store

// Умолчания настроек входа. Живут здесь, а не в пакете входа, потому что их
// читают двое: сам вход (`internal/auth`) и движок (адрес входа нужен
// жителю машины, пушам и входу на чужие серверы MCP), а движок пакет входа не
// тянет. Реестр один (setting_defs.go), и описание — одно на ключ; имена
// ключей в пакете входа (`auth.SettingRPID` и родня) проверяет
// `auth.TestSettingKeysMatchRegistry`.

// Все — осторожные (`Careful`): вход и доступ не правят «с ходу» с Обзора,
// только на своём экране.
var (
	// AuthPhraseLimit — неудач подряд с одного источника до запирания
	AuthPhraseLimit = Careful(DefPositive("auth.phrase_limit", 5, 1, 0))
	// AuthRPID — имя сервера для passkey; localhost, пока человек не назвал другое
	AuthRPID = Careful(DefText("auth.rp_id", "localhost"))
	// AuthOrigin — адрес входа; пусто — выводится из адреса прослушивания
	// (`auth.DefaultOrigin`), и такое умолчание зависит от запуска, а не от числа
	AuthOrigin = Careful(DefText("auth.origin", ""))
	// AuthLocalHosts — имена из своей сети, кроме адреса входа; пусто — только он
	AuthLocalHosts = Careful(DefText("auth.local_hosts", ""))
	// AuthLocalOpen — пускать «эту машину» без проверки личности. У новой
	// установки выключено: исходники открыты, и умолчание «пускать» отдавало бы
	// Сёму первому встречному из любой сети, где его видно. Обновлённому
	// Сёме с данными прежнее «включено» записывает разовая миграция
	// (`settleAuthDefaults`), а не это умолчание
	AuthLocalOpen = Careful(DefFlag("auth.local_open", false))
	// AuthLocalScope — что считать «этой машиной»: `loopback` (только петля)
	// или `private` (ещё и вся частная сеть — соседи по Wi-Fi, контейнеры на
	// той же машине). Частная сеть становится своей только по выбору человека
	AuthLocalScope = Careful(DefText("auth.local_scope", ScopeLoopback))
)

// Что считать «этой машиной» (`AuthLocalScope`).
const (
	ScopeLoopback = "loopback"
	ScopePrivate  = "private"
)
