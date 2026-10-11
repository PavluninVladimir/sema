package engine

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Табличные тесты класса чувствительности: что считать секретным, что
// чувствительным, а что обычным.
func TestSentClass(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"скан паспорта во вложении", store.ClassSecret},
		{"СНИЛС 123-456", store.ClassSecret},
		{"диагноз в выписке", store.ClassSecret},
		{"счёт на оплату хостинга", store.ClassSensitive},
		{"письмо от owner@example.org", store.ClassSensitive},
		{"как работает apt-get upgrade", store.ClassPlain},
		{"", store.ClassPlain},
	}
	for _, c := range cases {
		if got := sentClass(c.text); got != c.want {
			t.Errorf("sentClass(%q) = %q, хотели %q", c.text, got, c.want)
		}
	}
}

// Инвариант: секретное сильнее чувствительного, в каком бы порядке слова ни
// стояли. Сгенерированные случаи: все пары примет в обоих порядках.
func TestSentClassSecretWins(t *testing.T) {
	secrets := []string{"паспорт", "снилс", "cvv", "iban", "диагноз"}
	soft := []string{"счёт", "оплата", "сумма", "телефон", "адрес"}
	for _, s := range secrets {
		for _, w := range soft {
			for _, text := range []string{s + " и " + w, w + " и " + s, strings.ToUpper(s) + " " + w} {
				if got := sentClass(text); got != store.ClassSecret {
					t.Errorf("sentClass(%q) = %q, хотели секретное", text, got)
				}
			}
		}
	}
}

func TestSentSources(t *testing.T) {
	prompt := "Контекст: письмо #85 от банка, файл #42 договор, на сервере srv-bots упала служба. Memory: ничего."
	got := sentSources(prompt)
	for _, want := range []string{"письма 85", "файлы 42", "сервер srv-bots", "память"} {
		if !strings.Contains(got, want) {
			t.Errorf("sentSources = %q, нет %q", got, want)
		}
	}
}

func TestHumanCount(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1500: "1.5 тыс.", 2_400_000: "2.4 млн"}
	for n, want := range cases {
		if got := humanCount(n); got != want {
			t.Errorf("humanCount(%d) = %q, хотели %q", n, got, want)
		}
	}
}

// Фаззинг: что бы ни пришло, класс всегда один из трёх, и функция не падает.
func FuzzSentClass(f *testing.F) {
	f.Add("паспорт")
	f.Add("счёт на 1000 ₽")
	f.Add("обычный текст")
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		got := sentClass(text)
		switch got {
		case store.ClassPlain, store.ClassSensitive, store.ClassSecret:
		default:
			t.Fatalf("неизвестный класс %q для %q", got, text)
		}
	})
}
