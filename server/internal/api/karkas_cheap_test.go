package api

import (
	"testing"
	"time"
)

// Счётчики колонки настроек собираются на каждом экране настроек, а экран
// перечитывается опросом: каждая сборка раздела обязана быть дешёвой. Порог
// щедрый — это не замер скорости, а сторож от N+1 (на «Здоровье» расход
// когда-то считался тремя запросами на строку). Настоящий замер — на той
// машине, где больно (Raspberry Pi), а не здесь: pprof в сервер не подключён.
//
// На пустой базе мака дороже всех «Инструменты» (≈18 мс: проверка системного
// распознавания), остальные — десятки микросекунд.
func TestEachSectionMarkIsCheap(t *testing.T) {
	s, _ := testServer(t)
	sections := map[string]func() settingsSection{
		"mail": s.overviewMail, "telegram": s.overviewTelegram, "max": s.overviewMAX, "translate": s.overviewTranslate,
		"notify": s.overviewNotifications, "models": s.overviewModels, "search": s.overviewSearch, "skills": s.overviewSkills,
		"learning": s.overviewLearning, "tools": s.overviewTools, "hosts": s.overviewHosts, "agents": s.overviewAgents,
		"integrations": s.overviewIntegrations, "storages": s.overviewStorages, "access": s.overviewAccess,
		"secrets": s.overviewSecrets, "sent": s.overviewSent, "log": s.overviewSettingsLog, "watch": s.overviewWatch,
	}
	const times = 20
	var total time.Duration
	for name, f := range sections {
		start := time.Now()
		for i := 0; i < times; i++ {
			f()
		}
		avg := time.Since(start) / times
		total += avg
		t.Logf("%-13s %v", name, avg)
		if avg > 100*time.Millisecond {
			t.Errorf("пометка раздела %q собирается %v — на каждом экране настроек это слишком много", name, avg)
		}
	}
	t.Logf("все разделы: %v", total)
}
