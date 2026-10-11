package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Источник остатка — у провайдера: документированный адрес (DeepSeek,
// OpenRouter) или оценка «было минус расход Сёмы».

// OpenRouter: остаток — разность купленного и потраченного; нужен ключ
// управления, обычный получает 403, и это сказано прямо.
func TestBalanceOpenRouterCredits(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		if auth != "Bearer ключ-управления" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Only management keys can perform this operation"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"total_credits":100.5,"total_usage":25.75}}`))
	}))
	defer srv.Close()

	o := askCredits(context.Background(), "OpenRouter", srv.URL+"/api/v1", "ключ-управления")
	if path != "/api/v1/credits" || auth != "Bearer ключ-управления" {
		t.Fatalf("запрос: %s %s", path, auth)
	}
	if o.Err != "" || len(o.Amounts) != 1 || o.Amounts[0].Total != 74.75 || !o.Enough {
		t.Fatalf("остаток: %+v", o)
	}
	if s := o.Text(); !strings.Contains(s, "$74.75") || strings.Contains(s, "оценка") {
		t.Errorf("строка для человека: %q", s)
	}
	if o.Label() != "с провайдера" {
		t.Errorf("подпись источника: %q", o.Label())
	}

	// обычный ключ: 403 — честная строка, а не «ответ 403»
	o = askCredits(context.Background(), "OpenRouter", srv.URL+"/api/v1", "обычный-ключ")
	if !strings.Contains(o.Err, "ключу управления") || strings.Contains(o.Err, "403") {
		t.Errorf("403 без объяснения: %q", o.Err)
	}
	if !strings.Contains(o.Text(), "не узнать") {
		t.Errorf("строка вводит в заблуждение: %q", o.Text())
	}
	// ключа нет вовсе: не ходим, говорим, какой ключ нужен
	o = askCredits(context.Background(), "OpenRouter", srv.URL+"/api/v1", " ")
	if !strings.Contains(o.Err, "Management key") {
		t.Errorf("не сказано, какой ключ нужен: %q", o.Err)
	}
}

// rewriteTo — клиент, что шлёт любой запрос на тестовый сервер: адрес
// провайдера в настройках остаётся настоящим («openrouter.ai» решает,
// умеем ли мы спросить остаток), а сеть не трогается.
func rewriteTo(srv *httptest.Server) *http.Client {
	target, _ := url.Parse(srv.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r2 := r.Clone(r.Context())
		r2.URL.Scheme, r2.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r2)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Задача спрашивает OpenRouter ключом управления, а не ключом запросов, и
// кладёт ответ в кэш экрана.
func TestBalanceTaskAsksOpenRouterWithManagementKey(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization") + " " + r.URL.Path
		_, _ = w.Write([]byte(`{"data":{"total_credits":50,"total_usage":12.5}}`))
	}))
	defer srv.Close()
	was := balanceHTTP
	balanceHTTP = rewriteTo(srv)
	t.Cleanup(func() {
		balanceHTTP = was
		balancesMu.Lock()
		delete(balances, "OpenRouter")
		balancesMu.Unlock()
	})
	if err := e.AddModel(ModelConf{Name: "or-модель", Type: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "x/y", APIKey: "обычный"}); err != nil {
		t.Fatal(err)
	}
	// без ключа управления — строка про ключ, сеть не тронута
	if s := e.RefreshBalances(context.Background()); !strings.Contains(s, "Management key") || got != "" {
		t.Fatalf("без ключа управления: %q (запрос %q)", s, got)
	}
	if err := e.SetManagementKey("OpenRouter", "ключ-управления"); err != nil {
		t.Fatal(err)
	}
	balancesMu.Lock()
	delete(balances, "OpenRouter")
	balancesMu.Unlock()
	s := e.RefreshBalances(context.Background())
	if !strings.Contains(s, "$37.50") || got != "Bearer ключ-управления /api/v1/credits" {
		t.Fatalf("итог %q, запрос %q", s, got)
	}
	from := e.BalancesFromCache()
	if len(from) != 1 || from[0].Kind != BalanceAPI || !strings.Contains(from[0].Text(), "$37.50") {
		t.Errorf("кэш экрана: %+v", from)
	}
	// ключ управления не тот, что для запросов: значение из хранилища не смешивается
	if e.ProviderKey("OpenRouter") != "" || e.ManagementKey("OpenRouter") != "ключ-управления" {
		t.Error("ключи провайдера перепутаны")
	}
}

// «Гадать нельзя»: у провайдеров без документированного адреса остаток
// не спрашивается, даже если источник переключили на api.
func TestBalanceAPIOnlyWhereDocumented(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.SetBalanceSource("OpenAI", BalanceAPI, 0); err == nil {
		t.Error("OpenAI разрешили спрашивать остаток по адресу, которого в документации нет")
	}
	if err := e.SetBalanceSource("OpenAI", BalanceEstimate, 5); err != nil {
		t.Errorf("оценка у OpenAI: %v", err)
	}
	for _, name := range []string{"OpenAI", "Anthropic", "Perplexity", "Google Gemini"} {
		if p, _ := e.Provider(name); p.BalanceKind != BalanceEstimate {
			t.Errorf("%s: источник остатка %q, ждали оценку", name, p.BalanceKind)
		}
	}
	for _, name := range []string{"DeepSeek", "OpenRouter"} {
		if p, _ := e.Provider(name); p.BalanceKind != BalanceAPI {
			t.Errorf("%s: источник остатка %q, ждали api", name, p.BalanceKind)
		}
	}
	for _, name := range []string{"LM Studio (в своей сети)", "Ollama (в своей сети)"} {
		if p, _ := e.Provider(name); p.BalanceKind == BalanceAPI || p.BalanceKind == BalanceEstimate {
			t.Errorf("%s: у своей машины остатка на счёте нет", name)
		}
	}
}

// estimateRig — провайдер OpenAI с двумя моделями: одна с ценой и расходом,
// другая без цены, а вызовы были.
func estimateRig(t *testing.T) (*Engine, *store.Store) {
	e, st := newTestEngine(t, "", nil)
	for _, m := range []ModelConf{
		{Name: "gpt-с-ценой", Model: "gpt-1", Price: "$1 / $2 / 1M"},
		{Name: "gpt-без-цены", Model: "gpt-2"},
	} {
		m.Type, m.BaseURL = "openai", "https://api.openai.com/v1"
		if err := e.AddModel(m); err != nil {
			t.Fatal(err)
		}
	}
	return e, st
}

func addSpend(t *testing.T, st *store.Store, model string, cost float64, calls int) {
	t.Helper()
	for i := 0; i < calls; i++ {
		if err := st.AddUsage(&store.UsageRow{Consumer: "core:test", Kind: "llm", Model: model, OK: true,
			TokensIn: 100, TokensOut: 50, Cost: cost / float64(calls), Currency: "$"}); err != nil {
			t.Fatal(err)
		}
	}
}

// Оценка: было $10 на дату, расход Сёмы $2.50 — «≈ $7.50 (оценка)» с
// оговоркой «траты вне Сёмы не видны». Вызовы без цены названы: часть
// расхода не посчитана.
func TestBalanceEstimate(t *testing.T) {
	e, st := estimateRig(t)
	// до ввода «было» — не число, а просьба его ввести
	if b := e.estimateBalance(mustProvider(t, e, "OpenAI")); !b.At.IsZero() || !strings.Contains(b.Text(), "впишите") {
		t.Fatalf("оценка до ввода: %q", b.Text())
	}

	addSpend(t, st, "gpt-с-ценой", 2.5, 5)
	addSpend(t, st, "gpt-без-цены", 0, 3)
	if err := e.SetBalanceBase("OpenAI", "$10", time.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	b := e.estimateBalance(mustProvider(t, e, "OpenAI"))
	if b.Kind != BalanceEstimate || len(b.Amounts) != 1 || b.Amounts[0].Total != 7.5 || !b.Enough {
		t.Fatalf("оценка: %+v", b)
	}
	text := b.Text()
	for _, want := range []string{"≈ $7.50", "оценка", "было $10.00", "минус расход Сёмы $2.50",
		"траты вне Сёмы не видны", "часть расхода не посчитана: 3 вызова без цены"} {
		if !strings.Contains(text, want) {
			t.Errorf("в оценке нет %q: %q", want, text)
		}
	}
	if b.Label() != "оценка" {
		t.Errorf("подпись источника: %q", b.Label())
	}
	// оценка не ходит в сеть и не выдаёт себя за слово провайдера
	if strings.Contains(text, "с провайдера") {
		t.Errorf("оценка выдана за слово провайдера: %q", text)
	}
}

// Расход до даты «было» в оценку не идёт.
func TestBalanceEstimateCountsFromDate(t *testing.T) {
	e, st := estimateRig(t)
	old := time.Now().AddDate(0, 0, -3)
	if err := st.AddUsage(&store.UsageRow{Consumer: "core:test", Kind: "llm", Model: "gpt-с-ценой", OK: true, At: old, Cost: 4, Currency: "$"}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetBalanceBase("OpenAI", "10", time.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if b := e.estimateBalance(mustProvider(t, e, "OpenAI")); b.Amounts[0].Total != 10 {
		t.Errorf("расход до даты «было» вычтен: %+v", b.Amounts)
	}
	// ввод сломанной даты и суммы — отказ с объяснением, а не тихий ноль
	if err := e.SetBalanceBase("OpenAI", "много", ""); err == nil {
		t.Error("сумма «много» принята")
	}
	if err := e.SetBalanceBase("OpenAI", "10", "вчера"); err == nil {
		t.Error("дата «вчера» принята")
	}
	if err := e.SetBalanceBase("OpenAI", "10", time.Now().AddDate(0, 1, 0).Format("2006-01-02")); err == nil {
		t.Error("дата в будущем принята")
	}
}

func mustProvider(t *testing.T, e *Engine, name string) ProviderConf {
	t.Helper()
	p, ok := e.Provider(name)
	if !ok {
		t.Fatalf("провайдера %s нет", name)
	}
	return p
}

func openBalanceCard(st *store.Store, provider string) (*store.Notification, bool) {
	return st.OpenNotificationByKey("balance:" + provider)
}

// Порог: ниже него — предупреждение (для оценки — с пометкой «по оценке»);
// одно на переход, а не каждые полчаса; отложенное не возвращается, пока
// остаток не выправится; новое «было» карточку закрывает.
func TestBalanceThresholdNotifies(t *testing.T) {
	e, st := estimateRig(t)
	addSpend(t, st, "gpt-с-ценой", 2.5, 1)
	if err := e.SetBalanceBase("OpenAI", "10", time.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if err := e.SetBalanceSource("OpenAI", BalanceEstimate, 8); err != nil {
		t.Fatal(err)
	}
	e.RefreshBalances(context.Background())
	n, ok := openBalanceCard(st, "OpenAI")
	if !ok {
		t.Fatal("остаток $7.50 при пороге $8 — предупреждения нет")
	}
	if n.Level != "warn" || !strings.Contains(n.Title, "по оценке") {
		t.Errorf("уровень %q, заголовок %q — оценка не помечена", n.Level, n.Title)
	}
	firstID := n.ID

	// вторая проверка того же состояния: карточка та же, второй нет
	e.RefreshBalances(context.Background())
	if n2, _ := openBalanceCard(st, "OpenAI"); n2 == nil || n2.ID != firstID {
		t.Error("предупреждение поднято заново на том же состоянии")
	}

	// человек отложил: пока остаток ниже порога, карточка не возвращается
	if err := st.SetNotificationState(firstID, store.NotifyDismissed, "Вадим"); err != nil {
		t.Fatal(err)
	}
	e.RefreshBalances(context.Background())
	if _, ok := openBalanceCard(st, "OpenAI"); ok {
		t.Error("отложенное предупреждение вернулось с тем же состоянием")
	}

	// пополнили: новое «было» — и предупреждения нет; снова упало — новое
	if err := e.SetBalanceBase("OpenAI", "$50", time.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	e.RefreshBalances(context.Background())
	if _, ok := openBalanceCard(st, "OpenAI"); ok {
		t.Error("остаток в норме, а предупреждение висит")
	}
	if err := e.SetBalanceBase("OpenAI", "$9", time.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	e.RefreshBalances(context.Background())
	if _, ok := openBalanceCard(st, "OpenAI"); !ok {
		t.Error("остаток снова упал ниже порога, а предупреждения нет")
	}
	// новое «было» закрывает карточку сразу, не дожидаясь задачи
	if err := e.SetBalanceBase("OpenAI", "$100", time.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if _, ok := openBalanceCard(st, "OpenAI"); ok {
		t.Error("пополнение не закрыло предупреждение")
	}
}

// «Не хватает» у провайдера, что сам называет остаток, — тревога, как было;
// порог у api — по ответу провайдера, без пометки «по оценке».
func TestBalanceDeepSeekAlertAndThreshold(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	p := ProviderConf{Name: "DeepSeek", BalanceKind: BalanceAPI, BalanceLow: 5}

	e.balanceNotify(p, Balance{Provider: "DeepSeek", Kind: BalanceAPI, At: time.Now(), Enough: false,
		Amounts: []Amount{{Currency: "USD", Total: 0}}})
	n, ok := openBalanceCard(st, "DeepSeek")
	if !ok || n.Level != "alert" {
		t.Fatalf("«не хватает» — не тревога: %+v", n)
	}
	// тот же ответ — ни второй карточки, ни повторного оповещения
	e.balanceNotify(p, Balance{Provider: "DeepSeek", Kind: BalanceAPI, At: time.Now(), Enough: false,
		Amounts: []Amount{{Currency: "USD", Total: 0}}})
	if n2, _ := openBalanceCard(st, "DeepSeek"); n2.ID != n.ID {
		t.Error("тревога поднята заново")
	}
	// пополнили выше порога: карточка закрыта
	e.balanceNotify(p, Balance{Provider: "DeepSeek", Kind: BalanceAPI, At: time.Now(), Enough: true,
		Amounts: []Amount{{Currency: "USD", Total: 20}}})
	if _, ok := openBalanceCard(st, "DeepSeek"); ok {
		t.Error("остаток в норме, а тревога висит")
	}
	// достаточно, но ниже порога: предупреждение без «по оценке»
	e.balanceNotify(p, Balance{Provider: "DeepSeek", Kind: BalanceAPI, At: time.Now(), Enough: true,
		Amounts: []Amount{{Currency: "USD", Total: 3}}})
	n, ok = openBalanceCard(st, "DeepSeek")
	if !ok || n.Level != "warn" || strings.Contains(n.Title, "по оценке") {
		t.Fatalf("порог у api: %+v", n)
	}
	// не узнали (сбой сети) — про деньги не тревожим
	e.balanceNotify(p, Balance{Provider: "DeepSeek", Kind: BalanceAPI, At: time.Now(), Err: "ответ 500"})
	if n2, _ := openBalanceCard(st, "DeepSeek"); n2 == nil || n2.ID != n.ID {
		t.Error("сбой сети сбросил или подменил предупреждение")
	}
}
