package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Суммы у DeepSeek приходят строками — деньги считают строками, чтобы не
// терять копейки. Разобрать их надо, а не сделать вид, что там числа.
func TestBalanceParsed(t *testing.T) {
	var path, key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, key = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[
			{"currency":"USD","total_balance":"12.34","granted_balance":"2.00","topped_up_balance":"10.34"}]}`))
	}))
	defer srv.Close()

	o := askBalance(context.Background(), "DeepSeek", srv.URL+"/v1", "ключ-проба")
	if path != "/user/balance" {
		t.Fatalf("запрос ушёл на %q", path)
	}
	if key != "Bearer ключ-проба" {
		t.Fatalf("ключ не подставился: %q", key)
	}
	if o.Err != "" {
		t.Fatalf("ошибка на живом ответе: %s", o.Err)
	}
	if len(o.Amounts) != 1 {
		t.Fatalf("сумм разобрано %d", len(o.Amounts))
	}
	if s := o.Amounts[0]; s.Total != 12.34 || s.Granted != 2.00 || s.ToppedUp != 10.34 || s.Currency != "USD" {
		t.Fatalf("разобрано неверно: %+v", s)
	}
	if s := o.Text(); !strings.Contains(s, "$12.34") || !strings.Contains(s, "подарено $2.00") {
		t.Fatalf("строка для человека: %q", s)
	}
}

// Денег не хватает — это тревога, а не мелкая строчка: следующий вызов
// модели просто не пройдёт.
func TestBalanceNotEnough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"is_available":false,"balance_infos":[
			{"currency":"CNY","total_balance":"0.00","granted_balance":"0.00","topped_up_balance":"0.00"}]}`))
	}))
	defer srv.Close()
	o := askBalance(context.Background(), "DeepSeek", srv.URL, "к")
	if o.Enough {
		t.Fatal("нехватка не замечена")
	}
	if !strings.Contains(o.Text(), "не хватает") {
		t.Fatalf("человеку не сказано: %q", o.Text())
	}
}

// Без ключа не ходим вовсе, а ошибку называем понятно: молчаливая пустая
// строка выглядит как «денег ноль».
func TestBalanceWithoutKey(t *testing.T) {
	o := askBalance(context.Background(), "DeepSeek", "https://api.deepseek.com/v1", "  ")
	if !strings.Contains(o.Err, "ключ") {
		t.Fatalf("причина непонятная: %q", o.Err)
	}
	if !strings.Contains(o.Text(), "не узнать") {
		t.Fatalf("строка вводит в заблуждение: %q", o.Text())
	}
}

// Спрашиваем только тех, у кого адрес документирован: гадать по
// недокументированным нельзя — они меняются молча.
func TestBalanceOnlyOfCapable(t *testing.T) {
	if !canBalance("https://api.deepseek.com/v1") {
		t.Fatal("DeepSeek должен уметь")
	}
	for _, u := range []string{"https://api.perplexity.ai", "http://llm:8080/v1", "https://api.openai.com/v1"} {
		if canBalance(u) {
			t.Fatalf("%s не обещал такого адреса", u)
		}
	}
}

// Счёт у DeepSeek бывает и в юанях, и в долларах — иногда одновременно.
// Показать одну валюту и выбросить вторую значит показать человеку не его
// деньги; пересчитывать по своему курсу — тем более.
func TestBalanceInSeveralCurrencies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[
			{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"},
			{"currency":"USD","total_balance":"5.50","granted_balance":"0.00","topped_up_balance":"5.50"}]}`))
	}))
	defer srv.Close()
	o := askBalance(context.Background(), "DeepSeek", srv.URL+"/v1", "к")
	if len(o.Amounts) != 2 {
		t.Fatalf("валют разобрано %d — вторая потерялась", len(o.Amounts))
	}
	s := o.Text()
	if !strings.Contains(s, "¥110.00") {
		t.Fatalf("юани не показаны: %q", s)
	}
	if !strings.Contains(s, "$5.50") {
		t.Fatalf("доллары не показаны: %q", s)
	}
	if !strings.Contains(s, "подарено ¥10.00") {
		t.Fatalf("подаренное в юанях потерялось: %q", s)
	}
}

// Знак незнакомой валюты не выдумываем: «$» вместо юаня — враньё о сумме.
func TestSignCurrency(t *testing.T) {
	pairs := map[string]string{"USD": "$", "CNY": "¥", "cny": "¥", "RUB": "₽", "EUR": "€", "JPY": "JPY "}
	for code, sign := range pairs {
		if got := CurrencySign(code); got != sign {
			t.Errorf("%s → %q, ждали %q", code, got, sign)
		}
	}
}

// Экран «Здоровье» больше не ходит к провайдеру: остаток спрашивает задача
// сердцебиения, а экран показывает уже известное. Первый заход стоил четырёх
// секунд ожидания сети, и раз в десять минут кто-то платил их своим временем.
func TestBalanceScreenNotGoesInNet(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	// адрес подменять нельзя: «умеет остаток» решается по нему. Значит если
	// экран всё-таки пойдёт в сеть, он пойдёт к настоящему DeepSeek — и
	// вернётся либо с ошибкой, либо с ответом, но в обоих случаях со
	// временем запроса. Пустое «Когда» и есть доказательство, что не ходил
	if err := e.AddModel(ModelConf{Name: "дипсик", Type: "openai",
		BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"}); err != nil {
		t.Fatalf("модель: %v", err)
	}

	from := e.BalancesFromCache()
	if len(from) != 1 {
		t.Fatalf("провайдеров в кэше %d, ждали 1", len(from))
	}
	if !from[0].At.IsZero() {
		t.Fatal("экран сходил к провайдеру: у остатка проставлено время запроса")
	}
	// пока не спрашивали — говорим об этом прямо, а не показываем ноль
	if s := from[0].Text(); !strings.Contains(s, "ещё не спрашивал") {
		t.Fatalf("человеку сказано неправду: %q", s)
	}
	if strings.Contains(from[0].Text(), "ноль") {
		t.Fatal("ноль вместо «не знаю» — это враньё о деньгах")
	}

	// а задача — ходит и кладёт ответ в кэш
	balancesMu.Lock()
	balances["DeepSeek"] = Balance{Provider: "DeepSeek", At: time.Now(),
		Amounts: []Amount{{Currency: "USD", Total: 7}}, Enough: true}
	balancesMu.Unlock()
	t.Cleanup(func() {
		balancesMu.Lock()
		delete(balances, "DeepSeek")
		balancesMu.Unlock()
	})
	again := e.BalancesFromCache()
	if len(again) != 1 || again[0].At.IsZero() {
		t.Fatalf("кэш не читается: %+v", again)
	}
	if !strings.Contains(again[0].Text(), "$7.00") {
		t.Fatalf("остаток из кэша: %q", again[0].Text())
	}
}

// Задача не дёргает провайдера чаще, чем нужно: свежий ответ переиспользуется.
func TestBalanceTaskNotTooOften(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.AddModel(ModelConf{Name: "дипсик", Type: "openai",
		BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"}); err != nil {
		t.Fatalf("модель: %v", err)
	}
	balancesMu.Lock()
	balances["DeepSeek"] = Balance{Provider: "DeepSeek", At: time.Now(),
		Amounts: []Amount{{Currency: "USD", Total: 3}}, Enough: true}
	balancesMu.Unlock()
	t.Cleanup(func() {
		balancesMu.Lock()
		delete(balances, "DeepSeek")
		balancesMu.Unlock()
	})
	result := e.RefreshBalances(context.Background())
	if !strings.Contains(result, "$3.00") {
		t.Fatalf("свежий ответ не переиспользован: %q", result)
	}
}

// Провайдеров, называющих остаток, может не быть вовсе — тогда задача
// говорит об этом, а не молчит.
func TestBalanceWithoutProviders(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if from := e.BalancesFromCache(); len(from) != 0 {
		t.Fatalf("взялись остатки из ниоткуда: %+v", from)
	}
	if result := e.RefreshBalances(context.Background()); !strings.Contains(result, "нет") {
		t.Fatalf("итог задачи: %q", result)
	}
}
