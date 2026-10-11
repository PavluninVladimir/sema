package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/llm"
)

// stubFal — fal.ai без денег и сети: каталог, цены, очередь, картинка.
// «дешёвая-негодная» дешевле всех, но отвергает запрос — проверяем, что
// Сёма берёт следующую по цене, а не падает.
func stubFal(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var requestsDraw int32
	png := []byte("\x89PNG\r\n\x1a\nкартинка")
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") != "text-to-image" {
			t.Errorf("каталог спрошен не по категории text-to-image: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{
			{"endpoint_id": "fal-ai/дорогая"}, {"endpoint_id": "fal-ai/дешёвая-негодная"},
			{"endpoint_id": "fal-ai/средняя"}, {"endpoint_id": "fal-ai/за-секунды"},
		}, "has_more": false})
	})
	mux.HandleFunc("/v1/models/pricing", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Key ключ-fal" {
			t.Errorf("цены спрошены без ключа: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"prices": []map[string]any{
			{"endpoint_id": "fal-ai/дорогая", "unit_price": 0.05, "unit": "image", "currency": "USD"},
			{"endpoint_id": "fal-ai/дешёвая-негодная", "unit_price": 0.001, "unit": "megapixels", "currency": "USD"},
			{"endpoint_id": "fal-ai/средняя", "unit_price": 0.003, "unit": "image", "currency": "USD"},
			// за секунды GPU цену картинки заранее не посчитать — такую не берём
			{"endpoint_id": "fal-ai/за-секунды", "unit_price": 0.00001, "unit": "gpu-second", "currency": "USD"},
		}})
	})
	mux.HandleFunc("/q/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/q/")
		switch {
		case r.Method == http.MethodPost && path == "fal-ai/дешёвая-негодная":
			atomic.AddInt32(&requestsDraw, 1)
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":"image_size не поддерживается"}`))
		case r.Method == http.MethodPost:
			atomic.AddInt32(&requestsDraw, 1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["prompt"] == "" {
				t.Error("в fal ушёл запрос без описания")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status_url": srv.URL + "/q/" + path + "/requests/1/status", "response_url": srv.URL + "/q/" + path + "/requests/1",
			})
		case strings.HasSuffix(path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "COMPLETED"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"images": []map[string]string{{"url": srv.URL + "/img.png", "content_type": "image/png"}}})
		}
	})
	mux.HandleFunc("/img.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	srv = httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	wasAPI, wasQueue, wasClient := falAPI, falQueue, falHTTP
	falAPI, falQueue, falHTTP = srv.URL+"/v1", srv.URL+"/q", srv.Client()
	t.Cleanup(func() { falAPI, falQueue, falHTTP = wasAPI, wasQueue, wasClient })
	return srv, &requestsDraw
}

func TestImageByCheapestModel(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	_, draws := stubFal(t)
	if err := e.SetProviderKey(FalProvider, "ключ-fal"); err != nil {
		t.Fatal(err)
	}
	ctx := llm.WithConsumer(context.Background(), "module:techforum")

	ready, err := e.DrawImage(ctx, "робот-программист за ноутбуком, неоновый свет")
	if err != nil {
		t.Fatalf("картинка не нарисовалась: %v", err)
	}
	// самая дешёвая отвергла запрос — взята следующая по цене, а не дорогая
	if ready.Model.Endpoint != "fal-ai/средняя" {
		t.Errorf("нарисовала %q, а самая дешёвая из годных — fal-ai/средняя", ready.Model.Endpoint)
	}
	if !strings.Contains(st.Setting("images.unfit", ""), "fal-ai/дешёвая-негодная") {
		t.Error("отвергнувшая запрос модель не помечена негодной — её будут пробовать снова")
	}
	if ready.File == nil || ready.File.ID == 0 || !strings.HasPrefix(ready.File.MIME, "image/") {
		t.Fatalf("картинка не сохранилась в файлы: %+v", ready.File)
	}
	// публичный адрес от fal сохранён: по нему Telegram покажет превью над
	// длинным постом
	if addr := e.ImageAddr(ready.File.ID); !strings.HasSuffix(addr, "/img.png") {
		t.Errorf("публичный адрес картинки не сохранён: %q", addr)
	}

	// расход записан деньгами на того, кто просил
	line, err := st.UsageBy("consumer", ready.File.Date.AddDate(0, 0, -1), "image")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range line {
		if s.Key == "module:techforum" && s.Cost > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("стоимость картинки не записана в расход модуля: %+v", line)
	}

	// второй раз — без нового подбора: цены проверяются раз в сутки
	was := atomic.LoadInt32(draws)
	if _, err := e.DrawImage(ctx, "вторая картинка"); err != nil {
		t.Fatalf("вторая картинка: %v", err)
	}
	if atomic.LoadInt32(draws)-was != 1 {
		t.Errorf("на вторую картинку ушло %d запросов рисования, а нужен один", atomic.LoadInt32(draws)-was)
	}
}

// Описание уходит из дома, поэтому секретное не отправляется вовсе — ни
// одного запроса в fal.
func TestSecretDescriptionNotLeavesInFal(t *testing.T) {
	e, _ := newTestEngine(t, "модель", nil)
	_, draws := stubFal(t)
	if err := e.SetProviderKey(FalProvider, "ключ-fal"); err != nil {
		t.Fatal(err)
	}
	_, err := e.DrawImage(context.Background(), "паспорт серия 4510 номер 123456, выдан ОВД")
	if err == nil || !strings.Contains(err.Error(), "секрет") {
		t.Fatalf("секретное описание не остановлено: %v", err)
	}
	if atomic.LoadInt32(draws) != 0 {
		t.Error("секретное описание всё-таки ушло в fal")
	}
}

// Без ключа — понятный отказ с тем, куда его ввести.
func TestImageWithoutKeyFal(t *testing.T) {
	e, _ := newTestEngine(t, "модель", nil)
	_, err := e.DrawImage(context.Background(), "рисунок")
	if err == nil || !strings.Contains(err.Error(), "ключа fal.ai") || !strings.Contains(err.Error(), "подключите провайдера") {
		t.Fatalf("без ключа ждали понятный отказ, а вышло: %v", err)
	}
}

func TestPriceForImage(t *testing.T) {
	for _, s := range []struct {
		unit  string
		price float64
		want  float64
		годна bool
	}{
		{"image", 0.003, 0.003, true},
		{"megapixels", 0.004, 0.004, true}, // 1280×720 ≈ 0,92 Мп → 1 Мп
		{"gpu-second", 0.001, 0, false},
		{"image", 0, 0, false},
	} {
		got, ok := priceForImage(s.unit, s.price)
		if ok != s.годна || (ok && got != s.want) {
			t.Errorf("ценаЗаКартинку(%q, %v) = %v, %v; ждали %v, %v", s.unit, s.price, got, ok, s.want, s.годна)
		}
	}
}

// fal.ai — обычный провайдер в общем справочнике: ключ вводится через
// «Подключить провайдера», а не отдельным разделом.
func TestFalInCommonRegistry(t *testing.T) {
	e, _ := newTestEngine(t, "модель", nil)
	p, ok := e.Provider(FalProvider)
	if !ok {
		t.Fatal("fal.ai нет в общем справочнике провайдеров")
	}
	if p.Kind != "fal" || !p.NeedKey || p.Catalog {
		t.Errorf("fal.ai описан не как провайдер картинок с ключом: %+v", p)
	}
}

// Режим «получше»: выбор только из списка сильных моделей (images.prefer), а
// не из всего каталога, — и из них самая дешёвая. Каталог в этом режиме не
// спрашивается вовсе.
func TestModeBetterPicksFromList(t *testing.T) {
	e, st := newTestEngine(t, "модель", nil)
	stubFal(t)
	if err := e.SetProviderKey(FalProvider, "ключ-fal"); err != nil {
		t.Fatal(err)
	}
	// в списке — дорогая и средняя; дешёвая-негодная в список не входит
	_ = st.SetSetting("images.prefer", "fal-ai/дорогая,fal-ai/средняя")
	if err := e.SetImageMode(ModeBetter); err != nil {
		t.Fatal(err)
	}
	m, err := e.PickImageModel(context.Background())
	if err != nil {
		t.Fatalf("подбор в режиме «получше»: %v", err)
	}
	if m.Endpoint != "fal-ai/средняя" {
		t.Errorf("в режиме «получше» выбрана %q, а ждали самую дешёвую из списка — fal-ai/средняя", m.Endpoint)
	}
	if m.Mode != ModeBetter {
		t.Errorf("выбранная модель не помнит режим: %+v", m)
	}

	// смена режима обратно — выбранная модель сбрасывается, подбор идёт заново
	if err := e.SetImageMode(ModeCheap); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := e.RememberedImageModel(); m.Endpoint != "" {
		t.Errorf("после смены режима осталась прежняя модель %q", m.Endpoint)
	}
}
