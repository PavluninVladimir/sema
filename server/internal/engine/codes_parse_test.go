package engine

// Табличный разбор значений кодов: по два примера на формат и мусор.
// Значения здесь настоящие по форме (так их и отдаёт читатель), сами
// номера выдуманы.

import (
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestParseCodes(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		value  string
		format string
		want   map[string]string
	}{
		{
			name: "чек ФНС", kind: "qr",
			value:  "t=20260406T1847&s=55830.00&fn=7380440902615257&i=4719&fp=3641530653&n=1",
			format: store.CodeFNS,
			want: map[string]string{"Дата": "06.04.2026 18:47", "Сумма": "55830.00",
				"ФН": "7380440902615257", "ФД": "4719", "ФП": "3641530653", "Вид расчёта": "приход"},
		},
		{
			name: "чек ФНС с секундами и возвратом", kind: "qr",
			value:  "t=20250131T090512&s=1200.5&fn=9999078902000123&i=12&fp=100500&n=2",
			format: store.CodeFNS,
			want:   map[string]string{"Сумма": "1200.50", "Дата": "31.01.2025 09:05", "Вид расчёта": "возврат прихода"},
		},
		{
			name: "чек ФНС без ФП — не чек", kind: "qr",
			value:  "t=20260406T1847&s=55830.00&fn=7380440902615257&i=4719",
			format: store.CodeText,
		},
		{
			name: "чек ФНС с невозможной датой", kind: "qr",
			value:  "t=20261306T1847&s=10.00&fn=1&i=1&fp=1",
			format: store.CodeText,
		},
		{
			name: "марка «Честного знака»", kind: "datamatrix",
			value:  "0104600439931256" + "21ABCDEFGHIJKLM" + "\x1d" + "93dGVz",
			format: store.CodeMark,
			want:   map[string]string{"GTIN": "04600439931256", "Серийный номер": "ABCDEFGHIJKLM", "Код проверки": "dGVz"},
		},
		{
			name: "марка с датой и партией", kind: "datamatrix",
			value:  "0104601234567890" + "17261231" + "10AB12" + "\x1d" + "2159qMuFCkjR1sT" + "\x1d" + "91EE06",
			format: store.CodeMark,
			want: map[string]string{"GTIN": "04601234567890", "Годен до": "261231",
				"Партия": "AB12", "Серийный номер": "59qMuFCkjR1sT", "Ключ проверки": "EE06"},
		},
		{
			name: "GS1 без серийного номера — не марка", kind: "datamatrix",
			value:  "010460043993125617261231",
			format: store.CodeText,
		},
		{
			name: "EAN-13", kind: "ean13", value: "4600682000150", format: store.CodeGTIN,
			want: map[string]string{"GTIN": "4600682000150"},
		},
		{
			name: "EAN-13 весовой", kind: "ean13", value: "2123456789010", format: store.CodeGTIN,
			want: map[string]string{"Признак": "внутренний код магазина (весовой товар)"},
		},
		{
			name: "EAN-13 с битой контрольной цифрой", kind: "ean13", value: "4600682000151", format: store.CodeText,
		},
		{
			name: "EAN-8", kind: "ean8", value: "96385074", format: store.CodeGTIN,
			want: map[string]string{"GTIN": "96385074"},
		},
		{
			name: "адрес", kind: "qr", value: "https://example.com/оплата/8842", format: store.CodeURL,
			want: map[string]string{"Адрес": "https://example.com/оплата/8842"},
		},
		{
			name: "адрес с токеном", kind: "qr", value: "https://pay.example.com/inv/8842?token=s3cr3t", format: store.CodeSecretURL,
			want: map[string]string{"Адрес": "адрес с секретом", "Куда ведёт": "pay.example.com"},
		},
		{
			name: "адрес с access_token", kind: "qr", value: "http://x.example.org/a?b=1&access_token=zzz", format: store.CodeSecretURL,
			want: map[string]string{"Адрес": "адрес с секретом"},
		},
		{
			name: "мусор", kind: "code128", value: "ЖЖЖ-42/бочка", format: store.CodeText,
		},
		{
			name: "пусто", kind: "qr", value: "   ", format: store.CodeText,
		},
		{
			name: "цифры, но не товарный код", kind: "code128", value: "4600682000150", format: store.CodeText,
		},
	}
	for _, s := range cases {
		t.Run(s.name, func(t *testing.T) {
			format, fields := parseCode(s.kind, s.value)
			if format != s.format {
				t.Fatalf("формат %q, ждали %q (поля %+v)", format, s.format, fields)
			}
			k := store.FileCode{Fields: fields}
			for signature, value := range s.want {
				if got := k.Field(signature); got != value {
					t.Errorf("поле %q = %q, ждали %q", signature, got, value)
				}
			}
		})
	}
}

// TestSecretFromAddrNotStored — значение с токеном не должно осесть ни в
// базе, ни в строке для модели: код приходит картинкой, но секрет от
// этого секретом быть не перестаёт.
func TestSecretFromAddrNotStored(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	k := e.parseValue(store.FileCode{Symbology: "qr",
		Value: "https://pay.example.com/inv/8842?token=s3cr3t-abcdef&sum=100"})
	if k.Format != store.CodeSecretURL {
		t.Fatalf("формат %q", k.Format)
	}
	if contains(k.Value, "s3cr3t") {
		t.Fatalf("секрет остался в значении: %q", k.Value)
	}
	if line := CodesForModel([]store.FileCode{k}); contains(line, "s3cr3t") {
		t.Fatalf("секрет уехал бы модели: %q", line)
	}
}

// TestMaskMemoryClosesAddr — маска, заведённая человеком в памяти,
// работает и на кодах: адрес с таким значением уходит как секретный.
func TestMaskMemoryClosesAddr(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := st.AddMemoryRule("mask", "мой-личный-ключ", "для теста"); err != nil {
		t.Fatalf("маска: %v", err)
	}
	k := e.parseValue(store.FileCode{Symbology: "qr",
		Value: "https://example.com/open?x=мой-личный-ключ"})
	if k.Format != store.CodeSecretURL {
		t.Fatalf("формат %q — маска памяти не сработала", k.Format)
	}
	if contains(k.Value, "мой-личный-ключ") {
		t.Fatalf("значение с маской сохранилось: %q", k.Value)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
