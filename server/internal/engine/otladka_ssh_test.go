package engine

import "testing"

// Сторож синтаксиса не должен просить человека за то, что ничего не
// меняет. 10 сентября 2026 задача «Утренняя проверка серверов» оставила
// шесть команд ждать решения, и половина из них была чистым чтением:
// «crontab -l 2>/dev/null» уходило к человеку, потому что «2>/dev/null»
// считалось вторым аргументом команды.
func TestHarmlessRedirectNotRequiresHuman(t *testing.T) {
	e, _ := rulesBench(t)
	reading := []string{
		`crontab -l 2>/dev/null`,
		`crontab -l`,
		`wg show 2>/dev/null`,
		`find /home -maxdepth 3 -name authorized_keys`,
	}
	for _, c := range reading {
		if policy, why := e.Classify(rulesHost, c); policy != PolicyAllow {
			t.Errorf("чтение ушло к человеку: %q → %s · %s", c, policy, why)
		}
	}
}

// А то, что действительно может изменить сервер, по-прежнему ждёт
// человека. Ослабить сторож здесь дороже, чем лишний раз спросить.
func TestDangerousStillWaitsHuman(t *testing.T) {
	e, _ := rulesBench(t)
	dangerous := []string{
		`find /home -maxdepth 3 -name authorized_keys -exec wc -l {} \;`, // -exec запускает команду
		`crontab -l > /tmp/копия`,                                        // запись в файл
		`crontab -l >> /etc/crontab`,                                     // дозапись в файл
		`wg show 2>&1 > /tmp/вывод`,                                      // запись под видом безобидного
		`sudo crontab -l`,                                                // sudo — только по «Спросить»
		`crontab -r`,                                                     // не «-l»
	}
	for _, c := range dangerous {
		if policy, _ := e.Classify(rulesHost, c); policy == PolicyAllow {
			t.Errorf("опасное принято за чтение: %q", c)
		}
	}
}

// «>» внутри кавычек — это текст, а не запись в файл: «awk '$3>=1000'»
// сравнивает числа. Проверяем сам разбор: круг у awk остаётся за
// человеком по другой причине — программу awk сторож не разбирает.
func TestQuotesNotCountedRedirect(t *testing.T) {
	words, quotes, ok := literalShellWordsQ(`awk -F: '$3>=1000'`)
	if !ok {
		t.Fatal("строка не разобрана")
	}
	if len(words) != 3 || words[2] != "$3>=1000" {
		t.Fatalf("слова: %v", words)
	}
	if !quotes[2] {
		t.Error("слово из кавычек не помечено — «>» в нём сочтут перенаправлением")
	}
	if quotes[0] || quotes[1] {
		t.Error("обычные слова помечены как закавыченные")
	}
}
