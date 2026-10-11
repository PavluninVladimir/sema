package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/netguard"
	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// Картинки к постам — через fal.ai. Своих моделей рисования у Сёмы нет, а
// на Pi картинку не нарисовать: машина пишет текст со скоростью 2,8 токена
// в секунду. fal держит сотни моделей «текст → картинка» с оплатой за
// результат, и владелец велел брать самую дешёвую.
//
// Самую дешёвую выбираем по настоящим ценам fal, а не по памяти: каталог
// моделей категории text-to-image и их цены приходят из Platform API и
// перепроверяются раз в сутки. Цена бывает за картинку, за мегапиксель или
// за секунду работы GPU. Последнюю заранее не посчитать — такие модели не
// берём: выдумать цену хуже, чем пропустить модель.

// FalProvider — провайдер из общего справочника. Ключ хранится как у всех
// провайдеров и вводится через «Подключить провайдера»; читает его только
// ядро — модуль получает картинку, но не ключ.
const FalProvider = "fal.ai"

// размер картинки к посту: 16:9, как превью в Telegram
const (
	imageW = 1280
	imageH = 720
)

// адреса fal — переменными: в тестах их подменяет сервер-заглушка
var (
	falAPI   = "https://api.fal.ai/v1"
	falQueue = "https://queue.fal.run"
	falHTTP  = netguard.Client(60 * time.Second)
)

// ImageModel — выбранная модель и цена одной картинки нашего размера.
type ImageModel struct {
	Endpoint string  `json:"endpoint"`
	Mode     string  `json:"mode,omitempty"`
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
	Unit     string  `json:"unit"`
}

// FalReady — есть ли ключ: без него картинки не рисуются вовсе.
func (e *Engine) FalReady() bool {
	return strings.TrimSpace(e.ProviderKey(FalProvider)) != ""
}

func (e *Engine) falKey(who string) (string, error) {
	v := strings.TrimSpace(e.ProviderKey(FalProvider))
	e.store.LogSecret(store.SecretUse{Name: providerSecret(FalProvider), Who: who, Granted: v != "", Note: "ядро · картинка к посту"})
	if v == "" {
		return "", fmt.Errorf("нет ключа fal.ai — подключите провайдера «fal.ai» в «Настройки → Модели и роли → Подключить провайдера»")
	}
	return v, nil
}

// priceForImage — цена одной картинки нашего размера в единицах модели.
// Мегапиксели fal считает с округлением вверх — берём так же, иначе
// дешёвая «за мегапиксель» модель выглядела бы дешевле, чем выйдет.
func priceForImage(unit string, price float64) (float64, bool) {
	u := strings.ToLower(strings.TrimSpace(unit))
	switch {
	case price <= 0:
		return 0, false
	case u == "image" || u == "images":
		return price, true
	case strings.Contains(u, "megapixel") || u == "mp":
		mp := math.Ceil(float64(imageW*imageH) / 1_000_000)
		return price * mp, true
	}
	return 0, false
}

func falGet(ctx context.Context, key, addr string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Key "+key)
	resp, err := falHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fal ответил %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(body)), 300))
	}
	return json.Unmarshal(body, v)
}

// Режимы выбора модели. «дешёвая» — самая дешёвая из всего каталога
// text-to-image, как велел владелец поначалу. «получше» — самая дешёвая из
// списка сильных моделей (images.prefer): у самых дешёвых картинка
// заметно слабее, и владелец попросил «получше» 17 сентября 2026. Меры
// качества у fal нет, поэтому список — настройка, и его можно править.
const (
	ModeCheap  = "дешёвая"
	ModeBetter = "получше"
)

// preferList — первичное заполнение images.prefer: сильные модели для
// иллюстраций из каталога fal на сентябрь 2026. Из них берётся самая
// дешёвая; модель, не принявшая наш запрос, пропускается.
const preferList = "fal-ai/flux-2-pro,bytedance/seedream/v5/pro/text-to-image,fal-ai/nano-banana-pro," +
	"fal-ai/recraft/v4/text-to-image,ideogram/v4,alibaba/qwen-image-3/text-to-image,openai/gpt-image-2,fal-ai/flux-2"

// ImageMode — как выбирается модель.
func (e *Engine) ImageMode() string {
	if e.store.Setting(ImageModeKey, "") == ModeBetter {
		return ModeBetter
	}
	return ModeCheap
}

// SetImageMode — сменить режим; выбранная модель сбрасывается и
// подберётся заново при следующей картинке или по кнопке.
func (e *Engine) SetImageMode(mode string) error {
	if mode != ModeBetter {
		mode = ModeCheap
	}
	_ = e.store.SetSetting("images.cheapest", "")
	return e.store.SetSetting(ImageModeKey, mode)
}

// ImageModelNow — модель, которой рисуем. Закреплённая человеком
// (images.model) главнее; иначе самая дешёвая, выбранная не раньше суток
// назад, иначе выбираем заново.
func (e *Engine) ImageModelNow(ctx context.Context) (ImageModel, error) {
	if pin := strings.TrimSpace(e.store.Setting("images.model", "")); pin != "" {
		return ImageModel{Endpoint: pin}, nil
	}
	var was ImageModel
	// запомненная модель, которая с тех пор отвергла запрос, устарела сразу,
	// а не через сутки: иначе её брали бы снова и снова
	if raw := e.store.Setting("images.cheapest", ""); raw != "" && json.Unmarshal([]byte(raw), &was) == nil && was.Endpoint != "" && !e.badModels()[was.Endpoint] && was.Mode == e.ImageMode() {
		if t, err := time.Parse(time.RFC3339, e.store.Setting("images.checked", "")); err == nil && time.Since(t) < 24*time.Hour {
			return was, nil
		}
	}
	return e.PickImageModel(ctx)
}

// PickImageModel — пройти каталог text-to-image, узнать цены и
// выбрать самую дешёвую. Модели, которые уже отказали нашему запросу
// (images.unfit), пропускаем.
func (e *Engine) PickImageModel(ctx context.Context) (ImageModel, error) {
	key, err := e.falKey("ядро · подбор модели картинок")
	if err != nil {
		return ImageModel{}, err
	}
	bad := e.badModels()
	mode := e.ImageMode()
	var model []string
	if mode == ModeBetter {
		for _, m := range strings.Split(e.store.Setting("images.prefer", preferList), ",") {
			if m = strings.TrimSpace(m); m != "" && !bad[m] {
				model = append(model, m)
			}
		}
	}
	cursor := ""
	for pages := 0; mode == ModeCheap && pages < 20; pages++ {
		q := url.Values{"category": {"text-to-image"}, "status": {"active"}, "limit": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var reply struct {
			Models []struct {
				EndpointID string `json:"endpoint_id"`
			} `json:"models"`
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		}
		if err := falGet(ctx, key, falAPI+"/models?"+q.Encode(), &reply); err != nil {
			return ImageModel{}, fmt.Errorf("каталог моделей fal: %w", err)
		}
		for _, m := range reply.Models {
			if m.EndpointID != "" && !bad[m.EndpointID] {
				model = append(model, m.EndpointID)
			}
		}
		if !reply.HasMore || reply.NextCursor == "" {
			break
		}
		cursor = reply.NextCursor
	}
	if len(model) == 0 {
		return ImageModel{}, fmt.Errorf("нет моделей для выбора: каталог fal пуст или весь список «получше» (images.prefer) отверг запрос")
	}

	var variants []ImageModel
	for i := 0; i < len(model); i += 50 {
		chunk := model[i:min(i+50, len(model))]
		var price struct {
			Prices []struct {
				EndpointID string  `json:"endpoint_id"`
				UnitPrice  float64 `json:"unit_price"`
				Unit       string  `json:"unit"`
				Currency   string  `json:"currency"`
			} `json:"prices"`
		}
		if err := falGet(ctx, key, falAPI+"/models/pricing?endpoint_id="+url.QueryEscape(strings.Join(chunk, ",")), &price); err != nil {
			return ImageModel{}, fmt.Errorf("цены fal: %w", err)
		}
		asked := map[string]bool{}
		for _, m := range chunk {
			asked[m] = true
		}
		for _, p := range price.Prices {
			// берём только то, о чём спрашивали, и не негодное: ответ цен
			// обязан совпадать с запросом, но полагаться на это нельзя
			if !asked[p.EndpointID] || bad[p.EndpointID] {
				continue
			}
			if c, ok := priceForImage(p.Unit, p.UnitPrice); ok {
				variants = append(variants, ImageModel{Endpoint: p.EndpointID, Price: c, Currency: p.Currency, Unit: p.Unit})
			}
		}
	}
	if len(variants) == 0 {
		return ImageModel{}, fmt.Errorf("у моделей fal нет цены за картинку или мегапиксель — выбирать не из чего")
	}
	sort.SliceStable(variants, func(i, j int) bool { return variants[i].Price < variants[j].Price })
	cheap := variants[0]
	cheap.Mode = mode
	if raw, err := json.Marshal(cheap); err == nil {
		_ = e.store.SetSetting("images.cheapest", string(raw))
		_ = e.store.SetSetting("images.checked", time.Now().Format(time.RFC3339))
	}
	log.Printf("картинки: самая дешёвая модель fal — %s, %.4f %s за картинку (из %d с понятной ценой)",
		cheap.Endpoint, cheap.Price, cheap.Currency, len(variants))
	return cheap, nil
}

// ImageReady — нарисованная и сохранённая картинка.
type ImageReady struct {
	File  *store.Attachment
	Model ImageModel
}

// DrawImage — картинка по описанию. Описание уходит из дома в fal,
// поэтому проходит ту же проверку, что отправка модели: секретное не
// уходит никогда, а сама отправка видна в «Что ушло».
func (e *Engine) DrawImage(ctx context.Context, description string) (*ImageReady, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return nil, fmt.Errorf("пустое описание картинки")
	}
	consumer := llm.ConsumerOf(ctx)
	if consumer == "" {
		consumer = "core"
	}
	class, why := sentClassWhy(description)
	if class == store.ClassSecret {
		return nil, fmt.Errorf("описание картинки похоже на секретное (%s) — наружу не отправляю", why)
	}
	if err := e.budgetCheck(ctx); err != nil {
		return nil, err
	}
	key, err := e.falKey(consumer)
	if err != nil {
		return nil, err
	}
	// самая дешёвая модель иногда не принимает наш запрос — тогда она
	// помечается негодной и берётся следующая по цене
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		model, err := e.ImageModelNow(ctx)
		if err != nil {
			return nil, err
		}
		start := time.Now()
		data, mime, addr, err := falDraw(ctx, key, model.Endpoint, description)
		row := &store.UsageRow{Consumer: consumer, Kind: "image", Model: "fal:" + model.Endpoint,
			Seconds: time.Since(start).Seconds(), OK: err == nil, Cost: model.Price, Currency: model.Currency}
		sent := &store.SentRow{At: time.Now().Format(time.RFC3339), Consumer: consumer, Model: "fal:" + model.Endpoint,
			Outside: true, Untrusted: true, Kind: "image", Class: class, Seconds: row.Seconds, OK: err == nil,
			Preview: clip(description, 200), Prompt: description}
		if err != nil {
			row.Cost, row.Note, sent.Err = 0, err.Error(), err.Error()
		}
		_ = e.store.AddUsage(row)
		_ = e.store.AddSent(sent)
		if err == nil {
			a, _, err := e.AddFile(ctx, "картинка-"+time.Now().Format("20060102-150405")+imageExt(mime), mime, data,
				FileSource{Kind: "module", Ref: consumer}, nil)
			if err != nil {
				return nil, fmt.Errorf("картинка нарисована, но не сохранилась: %w", err)
			}
			// публичный адрес у fal — вторым источником: длинный пост в
			// Telegram показывает картинку превью по адресу, а не файлом
			if strings.HasPrefix(addr, "https://") {
				_ = e.store.AddFileSource(a.ID, "web", addr, "")
			}
			return &ImageReady{File: a, Model: model}, nil
		}
		var denial *falRequestDenial
		if !asFalDenial(err, &denial) || strings.TrimSpace(e.store.Setting("images.model", "")) != "" {
			return nil, err
		}
		last = err
		e.markBad(model.Endpoint)
	}
	return nil, fmt.Errorf("три самые дешёвые модели fal не приняли запрос — подберите модель руками; последний отказ: %v", last)
}

// badModels — модели, которые отвергли наш запрос.
func (e *Engine) badModels() map[string]bool {
	bad := map[string]bool{}
	for _, m := range strings.Split(e.store.Setting("images.unfit", ""), ",") {
		if m = strings.TrimSpace(m); m != "" {
			bad[m] = true
		}
	}
	return bad
}

func (e *Engine) markBad(endpoint string) {
	was := e.store.Setting("images.unfit", "")
	if strings.Contains(","+was+",", ","+endpoint+",") {
		return
	}
	if was != "" {
		was += ","
	}
	_ = e.store.SetSetting("images.unfit", was+endpoint)
	log.Printf("картинки: модель %s не приняла запрос — больше её не берём", endpoint)
}

func imageExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	}
	return ".png"
}

// falRequestDenial — модель отвергла сам запрос (4xx): чаще всего не знает
// наших полей. Это не сбой связи, а повод взять следующую модель.
type falRequestDenial struct{ text string }

func (o *falRequestDenial) Error() string { return o.text }

func asFalDenial(err error, v **falRequestDenial) bool {
	o, ok := err.(*falRequestDenial)
	if ok {
		*v = o
	}
	return ok
}

// falDraw — запрос в очередь fal, ожидание и сама картинка.
func falDraw(ctx context.Context, key, endpoint, description string) ([]byte, string, string, error) {
	body, _ := json.Marshal(map[string]any{
		"prompt":     description,
		"image_size": map[string]int{"width": imageW, "height": imageH},
		"num_images": 1,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, falQueue+"/"+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("Authorization", "Key "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := falHTTP.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 == 4 && resp.StatusCode != 401 && resp.StatusCode != 403 && resp.StatusCode != 429 {
		return nil, "", "", &falRequestDenial{text: fmt.Sprintf("модель %s не приняла запрос (%d): %s", endpoint, resp.StatusCode, clip(string(raw), 300))}
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", "", fmt.Errorf("fal ответил %d: %s", resp.StatusCode, clip(string(raw), 300))
	}
	var job struct {
		StatusURL   string `json:"status_url"`
		ResponseURL string `json:"response_url"`
	}
	if err := json.Unmarshal(raw, &job); err != nil || job.StatusURL == "" || job.ResponseURL == "" {
		return nil, "", "", fmt.Errorf("fal не вернул адрес заявки: %s", clip(string(raw), 300))
	}

	for {
		var status struct {
			Status string `json:"status"`
		}
		if err := falGet(ctx, key, job.StatusURL, &status); err != nil {
			return nil, "", "", err
		}
		if status.Status == "COMPLETED" {
			break
		}
		select {
		case <-ctx.Done():
			return nil, "", "", fmt.Errorf("fal не успел нарисовать: %w", ctx.Err())
		case <-time.After(1500 * time.Millisecond):
		}
	}
	var result struct {
		Images []struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
		} `json:"images"`
		Detail any `json:"detail"`
	}
	if err := falGet(ctx, key, job.ResponseURL, &result); err != nil {
		return nil, "", "", err
	}
	if len(result.Images) == 0 || result.Images[0].URL == "" {
		return nil, "", "", &falRequestDenial{text: fmt.Sprintf("модель %s не вернула картинку: %v", endpoint, result.Detail)}
	}
	image, err := http.NewRequestWithContext(ctx, http.MethodGet, result.Images[0].URL, nil)
	if err != nil {
		return nil, "", "", err
	}
	reply, err := falHTTP.Do(image)
	if err != nil {
		return nil, "", "", fmt.Errorf("картинку нарисовали, но не скачать: %w", err)
	}
	defer reply.Body.Close()
	data, err := io.ReadAll(io.LimitReader(reply.Body, 15<<20))
	if err != nil || reply.StatusCode/100 != 2 {
		return nil, "", "", fmt.Errorf("картинку нарисовали, но не скачать (%d)", reply.StatusCode)
	}
	mime := result.Images[0].ContentType
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", "", fmt.Errorf("fal вернул не картинку, а %s", mime)
	}
	return data, mime, result.Images[0].URL, nil
}

// ImagePrice — цена картинки человеку: «$0.003 за картинку».
func ImagePrice(m ImageModel) string {
	if m.Price <= 0 {
		return "цена неизвестна"
	}
	currency := "$"
	if m.Currency != "" && m.Currency != "USD" {
		currency = m.Currency + " "
	}
	return currency + strconv.FormatFloat(m.Price, 'f', -1, 64) + " за картинку"
}

// toolImage — sema.image(prompt).
func (e *Engine) toolImage(ctx context.Context, a []any) (any, error) {
	ready, err := e.DrawImage(ctx, script.Str(a, 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"file": ready.File.ID, "model": ready.Model.Endpoint,
		"price": ready.Model.Price, "currency": ready.Model.Currency}, nil
}

// RememberedImageModel — что выбрано сейчас, без похода в сеть: для
// экрана. закреплена — модель задал человек, а не подбор.
func (e *Engine) RememberedImageModel() (m ImageModel, verified time.Time, pinned bool) {
	if pin := strings.TrimSpace(e.store.Setting("images.model", "")); pin != "" {
		return ImageModel{Endpoint: pin}, time.Time{}, true
	}
	if raw := e.store.Setting("images.cheapest", ""); raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	verified, _ = time.Parse(time.RFC3339, e.store.Setting("images.checked", ""))
	return m, verified, false
}

// ImageAddr — публичный адрес картинки у fal, если он известен: по
// нему Telegram показывает превью над длинным постом.
func (e *Engine) ImageAddr(fileID int64) string {
	for _, src := range e.store.FileSources(fileID) {
		if src.Kind == "web" && strings.HasPrefix(src.Ref, "https://") {
			return src.Ref
		}
	}
	return ""
}
