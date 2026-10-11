package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// ночной шаг осмотра, на котором всё и встало
const lookStep = "find /home -maxdepth 3 -name authorized_keys -exec md5sum {} + 2>/dev/null"

// Команда, вписанная человеком в «Разрешить» целиком, побеждает общие
// приметы. До 12 сентября 2026 ночной осмотр каждую ночь приносил
// человеку шаг «снять хэши файлов с ключами»: «find» с «-exec» помечен
// изменяющим (и правильно — «find -exec rm» стирает файлы), а список
// чтения до этой проверки не доходил. Кнопка «Добавить» на экране
// кругов обещала разрешить команду и не разрешала.
func TestCommandWholeAllowedHuman(t *testing.T) {
	e, st := rulesBench(t)
	// до разрешения: «find» по ключам меняет — а слово authorized_keys стоит у
	// сервера в «Спросить», так что шаг ждёт человека
	if policy, _ := e.Classify(rulesHost, lookStep); policy != PolicyAsk {
		t.Fatalf("до разрешения шаг должен ждать человека, а он %q", policy)
	}
	addRules(t, st, rulesHost, store.RuleAllow, lookStep)
	policy, why := e.Classify(rulesHost, lookStep)
	if policy != PolicyAllow {
		t.Errorf("человек разрешил команду целиком, а она всё равно %q (%s)", policy, why)
	}
}

// Но разрешение дано ровно этой строке, а не слову «find»: соседние
// виды той же утилиты остаются изменяющими.
func TestPermissionNotSpreadsOnNeighbors(t *testing.T) {
	e, st := rulesBench(t)
	addRules(t, st, rulesHost, store.RuleAllow, lookStep)
	dangerous := []string{
		"find /home -name '*.log' -exec rm {} +",
		"find / -name authorized_keys -delete",
		"find /home -maxdepth 3 -name authorized_keys -exec md5sum {} + 2>/dev/null && rm -rf /",
	}
	for _, cmd := range dangerous {
		if policy, _ := e.Classify(rulesHost, cmd); policy == PolicyAllow {
			t.Errorf("разрешение расползлось на соседнюю команду: %q", cmd)
		}
	}
}

// «Запретить» и sudo разрешение проигрывает: эти запреты выше решения
// человека про одну строку.
func TestPermissionWeakerNeverAndSudo(t *testing.T) {
	e, st := rulesBench(t)
	const withSudo = "sudo find /home -name authorized_keys -exec md5sum {} +"
	addRules(t, st, rulesHost, store.RuleAllow, withSudo)
	if policy, _ := e.Classify(rulesHost, withSudo); policy == PolicyAllow {
		t.Error("sudo прошёл как разрешённое — разрешение целиком оказалось сильнее sudo")
	}
	const forbidden = "reboot"
	addRules(t, st, rulesHost, store.RuleAllow, forbidden)
	if policy, _ := e.Classify(rulesHost, forbidden); policy != PolicyDeny {
		t.Errorf("команда из «Запретить» прошла как %q", policy)
	}
}

// «Спросить» держит изменения ключей, а не взгляд на них: читающая команда со
// словом authorized_keys к человеку не уходит, а изменяющая — уходит.
func TestReadingCommandNotLeavesHuman(t *testing.T) {
	e, st := rulesBench(t)
	addRules(t, st, rulesHost, store.RuleAllow, lookStep)
	if r := e.Decide(rulesHost, lookStep); r.Policy != PolicyAllow {
		t.Errorf("читающий шаг осмотра ушёл человеку: %s", r.Why)
	}
	// а изменяющая — спрашивается, как и раньше
	for _, cmd := range []string{
		"echo ssh-rsa AAAA >> /root/.ssh/authorized_keys",
		"sed -i '/ключ/d' /home/sema/.ssh/authorized_keys",
		"userdel sema",
	} {
		r := e.Decide(rulesHost, cmd)
		if r.Policy != PolicyAsk {
			t.Errorf("изменяющая команда перестала спрашиваться: %q → %s", cmd, r.Policy)
		} else if strings.TrimSpace(r.Why) == "" || len(r.Rules) == 0 {
			t.Errorf("не сказано, по какому правилу %q спрашивается", cmd)
		}
	}
}
