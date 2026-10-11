package api

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
)

// Действия с моделями, провайдерами, ролями и распознаванием.
// Часть общего разбора действий: execute перебирает такие группы, пока
// одна из них не возьмёт действие себе; чужое действие — errUnhandled.

func (s *Server) modelsActions(req actionRequest) (string, error) {
	switch req.Action {
	case "save_settings":
		// в списке модель показана вместе с провайдером — сохраняем имя
		if p := modelName(req.Params["provider"]); p != "" {
			return "", s.eng.SetActiveProvider(p)
		}
		return "", nil
	case "load_catalog":
		p, ok := s.eng.Provider(req.Params["provider"])
		if !ok {
			return "", fmt.Errorf("выберите провайдера")
		}
		key := strings.TrimSpace(req.Params["key"])
		// модели, подключённые пачкой, всегда внешние: доверие — утверждение
		// человека про конкретную модель и выдаётся на её экране, а один
		// тумблер у провайдера открыл бы закрытые ящики и секретное десяткам
		// моделей разом. Параметр `trusted` в этом запросе не читается вовсе
		if p.Kind == "fal" {
			// fal.ai рисует картинки, а не отвечает в чате: список его моделей
			// в роли не подключаем. Ключ проверяем сразу подбором самой
			// дешёвой модели — неверный ключ иначе всплыл бы на первом посте
			if key == "" {
				return "", fmt.Errorf("нужен ключ fal.ai — он на fal.ai/dashboard/keys")
			}
			if err := s.eng.SetProviderKey(p.Name, key); err != nil {
				return "", err
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := s.eng.PickImageModel(ctx); err != nil {
				return "", fmt.Errorf("ключ fal.ai сохранён, но модели и цены получить не вышло: %w", err)
			}
			return providerAddr(p.Name, providerTabKey), nil
		}
		if p.Kind == "cli" || !p.Catalog {
			// клиент по подписке и провайдер без списка (Perplexity, свой
			// шлюз) каталога не отдают — зря в сеть не ходим, спрашиваем
			// название модели у человека
			s.mu.Lock()
			s.catalog = catalogState{Provider: p.Name, Key: key}
			s.mu.Unlock()
			return "", nil
		}
		models, err := s.eng.Catalog(context.Background(), p, key)
		cat := catalogState{Provider: p.Name, Key: key, Models: models}
		if err != nil {
			cat.Err = err.Error()
		}
		s.mu.Lock()
		s.catalog = cat
		s.mu.Unlock()
		// ключ подошёл и модели есть — подключаем сразу все: человек
		// нажал «Подключить провайдера», а не «покажи список». Дальше — на
		// экран провайдера: там видно, что подключилось
		if err == nil && len(models) > 0 {
			if err := s.connectAll(cat); err != nil {
				return "", err
			}
			return providerAddr(p.Name, providerTabKey), nil
		}
		return "", nil
	case "quick_local_model":
		url := s.eng.LocalModelServer(context.Background())
		if url == "" {
			return "", fmt.Errorf("на этой машине сервер моделей не найден")
		}
		p, ok := s.eng.Provider("LM Studio (в своей сети)")
		if !ok {
			p = engine.ProviderConf{Name: "LM Studio (в своей сети)", Kind: "openai", Catalog: true}
		}
		p.BaseURL = url
		models, err := s.eng.Catalog(context.Background(), p, "")
		if err != nil || len(models) == 0 {
			return "", fmt.Errorf("сервер моделей не отдал список: %v", err)
		}
		pick := models[0]
		for _, m := range models { // эмбеддинги для разбора писем не годятся
			if !strings.Contains(m.ID, "embed") {
				pick = m
				break
			}
		}
		name := pick.ID
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		// кнопка «Подключить локальную модель» — сервер на этой машине, и
		// нажал её человек: это и есть его решение доверять
		return "setup", s.eng.AddModel(engine.ModelConf{
			Name: name, Type: "openai", BaseURL: url, Model: pick.ID, Trusted: true,
		})
	case "add_cli_model":
		s.mu.Lock()
		provider := s.catalog.Provider
		s.mu.Unlock()
		p, ok := s.eng.Provider(provider)
		if !ok {
			return "", fmt.Errorf("сначала выберите провайдера")
		}
		name := strings.TrimSpace(req.Params["name"])
		if name == "" {
			name = p.BaseURL
		}
		if err := s.eng.AddModel(engine.ModelConf{
			Name: name, Type: "cli", BaseURL: p.BaseURL, Model: p.BaseURL,
		}); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.catalog = catalogState{}
		s.mu.Unlock()
		return modelAddr(name), nil
	case "add_all_models":
		s.mu.Lock()
		cat := s.catalog
		s.mu.Unlock()
		if len(cat.Models) == 0 {
			return "", fmt.Errorf("список моделей пуст")
		}
		if err := s.connectAll(cat); err != nil {
			return "", err
		}
		return providerAddr(cat.Provider, providerTabKey), nil
	case "add_model_manual":
		s.mu.Lock()
		cat := s.catalog
		s.mu.Unlock()
		p, ok := s.eng.Provider(cat.Provider)
		if !ok {
			return "", fmt.Errorf("сначала выберите провайдера")
		}
		model := strings.TrimSpace(req.Params["model"])
		if model == "" {
			return "", fmt.Errorf("впишите название модели: у этого провайдера списка нет")
		}
		name := strings.TrimSpace(req.Params["name"])
		if name == "" {
			name = model
		}
		key := strings.TrimSpace(req.Params["key"])
		if key == "" {
			key = cat.Key // ключ мог быть введён на первом шаге
		}
		if key != "" {
			if err := s.eng.SetProviderKey(p.Name, key); err != nil {
				return "", err
			}
		}
		if err := s.eng.AddModel(engine.ModelConf{
			Name: name, Type: p.Kind, BaseURL: p.BaseURL, Model: model,
		}); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.catalog = catalogState{}
		s.mu.Unlock()
		return modelAddr(name), nil
	case "reset_catalog":
		s.mu.Lock()
		s.catalog = catalogState{}
		s.mu.Unlock()
		return "", nil
	case "add_model":
		s.mu.Lock()
		cat := s.catalog
		s.mu.Unlock()
		p, ok := s.eng.Provider(cat.Provider)
		if !ok {
			return "", fmt.Errorf("сначала выберите провайдера")
		}
		// в списке модель показана вместе с возможностями — берём имя до разделителя
		model, _, _ := strings.Cut(req.Params["model"], " · ")
		model = strings.TrimSpace(model)
		name := strings.TrimSpace(req.Params["name"])
		if name == "" {
			name = model
		}
		conf := engine.ModelConf{Trusted: req.Params["trusted"] == "true",
			Name: name, Type: p.Kind, BaseURL: p.BaseURL, Model: model, APIKey: cat.Key,
		}
		for _, m := range cat.Models { // возможности берём из каталога
			if m.ID == model {
				conf.Vision, conf.Hears, conf.Context, conf.Price = m.Vision, m.Hears, m.Context, m.Price
				break
			}
		}
		if err := s.eng.AddModel(conf); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.catalog = catalogState{}
		s.mu.Unlock()
		return modelAddr(name), nil
	case "dismiss_role_err":
		return modelsScreen + "~" + modelsTabIssues, s.eng.ClearRoleErr(req.Target)
	case "save_image_mode":
		if err := s.eng.SetImageMode(req.Params["value"]); err != nil {
			return "", err
		}
		// режим сменили — модель подбирается сразу, чтобы строка показала,
		// чем теперь рисуем и почём
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := s.eng.PickImageModel(ctx)
		return "", err
	case "pick_image_model":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := s.eng.PickImageModel(ctx)
		return "", err
	case "set_role_model":
		// в списке модель показана вместе с провайдером — в роль кладём
		// имя: роль указывает на модель, а не на строку экрана
		return "", s.eng.SetRoleField(req.Target, "model", modelName(req.Params["value"]))
	case "set_role_spare":
		return "", s.eng.SetRoleField(req.Target, "spare", modelName(req.Params["value"]))
	case "set_role_timeout":
		return "", s.eng.SetRoleField(req.Target, "timeout", req.Params["value"])
	case "save_speech_outside":
		return "", s.eng.SetSpeechOutside(modeKey(speechOutsideModes, req.Params["value"]))
	case "save_speech_language":
		return "", s.eng.SetSpeechLanguage(languageCode(req.Params["value"]))
	case "save_speech_files":
		return "", s.eng.SetSpeechFiles(modeKey(speechFilesModes, req.Params["value"]))
	case "toggle_model_hears":
		hears, byName := s.eng.ModelHearsBy(req.Target)
		if byName {
			return "", nil // умение видно по названию: снимать нечего
		}
		return "", s.eng.SetModelHears(req.Target, !hears)
	case "check_model":
		took, err := s.eng.CheckModel(context.Background(), req.Target)
		s.mu.Lock()
		if err != nil {
			s.lastCheck = req.Target + ": " + err.Error()
		} else {
			s.lastCheck = fmt.Sprintf("%s: ответила за %.1f с", req.Target, took.Seconds())
		}
		s.mu.Unlock()
		return "", nil
	case "toggle_model_trust":
		return "", s.eng.SetModelTrust(req.Target, !s.eng.ModelIsTrusted(req.Target))
	case "toggle_model_search":
		on := true
		for _, m := range s.eng.SearchModels() {
			if m == req.Target {
				on = false
			}
		}
		return "", s.eng.SetModelSearch(req.Target, on)
	case "connect_provider_models":
		// «обновить список» и «подключить новые» — одно действие: спросить
		// провайдера и не спросить, что у него есть сейчас, значит второй
		// раз наступить на то же самое
		key := s.eng.ProviderKey(req.Target)
		итог, err := s.eng.RefreshProviderModels(context.Background(), req.Target,
			func(models []engine.CatalogModel) (int, int, error) {
				return s.connectAllCounted(catalogState{Provider: req.Target, Key: key, Models: models})
			})
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.lastCheck = итог
		s.mu.Unlock()
		return "", nil
	case "remove_provider_models":
		n, err := s.eng.RemoveProviderModels(req.Target)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.lastCheck = fmt.Sprintf("%s: отключено моделей %d · ключ остался", req.Target, n)
		s.mu.Unlock()
		return "", nil
	case "toggle_provider_search":
		on := !s.eng.ProviderSearchOn(req.Target)
		n, err := s.eng.SetProviderSearch(req.Target, on)
		if err != nil {
			return "", err
		}
		word := "выключен"
		if on {
			word = "включен"
		}
		s.mu.Lock()
		s.lastCheck = fmt.Sprintf("%s: поиск %s у %d моделей", req.Target, word, n)
		s.mu.Unlock()
		return "", nil
	case "set_provider_key":
		return "", s.eng.SetProviderKey(req.Target, req.Params["value"])
	case "set_model_key":
		return "", s.eng.SetModelKey(req.Target, req.Params["value"])
	case "remove_model":
		if err := s.eng.RemoveModel(req.Target); err != nil {
			return "", err
		}
		// с экрана самой модели уходим к списку: показывать «модели нет» тому,
		// кто её только что удалил, незачем
		if strings.HasPrefix(req.Screen, modelsScreen+"@") && !strings.HasPrefix(req.Screen, modelsScreen+"@"+modelsProviderPrefix) {
			return modelsScreen + "~" + modelsTabList, nil
		}
		return "", nil
	case "set_models_limits":
		return "", s.saveSettings(req, settingParam{Param: "max_output", Key: "models.max_output", What: "предел ответа модели"})
	case "save_embeddings":
		changed, err := s.eng.SetEmbeddings(req.Params["model"], req.Params["base_url"])
		if err != nil {
			return "", err
		}
		if changed {
			log.Printf("эмбеддинги: модель сменилась на %q, индекс пересчитывается", req.Params["model"])
		}
		return "", nil
	case "toggle_embed_trust":
		return "", s.eng.SetEmbedTrust(!s.eng.EmbedTrusted())
	case "save_embed_model":
		_, err := s.eng.SetEmbeddings(req.Params["model"], s.eng.EmbedBaseURL())
		return "", err
	case "save_ocr_engine":
		return "", s.eng.SetOCREngine(req.Params["engine"])
	case "save_vision_model":
		s.eng.ResetVisionState()
		// в списке модель показана вместе с провайдером — в роль кладём имя
		return "", s.eng.SetVisionModel(modelName(req.Params["model"]))
	case "save_ocr":
		if err := s.eng.SetVisionModel(modelName(req.Params["model"])); err != nil {
			return "", err
		}
		s.eng.ResetVisionState()
		return "", s.eng.SetOCREngine(req.Params["engine"])
	case "set_price_name":
		if err := s.eng.SetPriceName(req.Target, req.Params["value"]); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.lastCheck = req.Target + ": связка с прайсом сохранена — цены будут узнаваться на странице провайдера"
		s.mu.Unlock()
		return "", nil
	case "set_model_price":
		return s.saveModelPrice(req)
	case "save_no_text":
		return "", s.eng.SetNoTextMode(noTextKeyOf(req.Params["mode"]))
	case "toggle_code_kind":
		включён := false
		for _, в := range s.eng.CodeKinds() {
			if в.Key == req.Target {
				включён = в.Enabled
			}
		}
		return "", s.eng.EnableCodeKind(req.Target, !включён)
	case "save_codes_limit":
		return "", s.eng.SetCodesLimit(numberFromCaption(req.Params["limit"]))
	case "save_codes_pixels":
		return "", s.eng.SetPixelsLimit(numberFromCaption(req.Params["limit"]))
	default:
		return "", errUnhandled
	}
}

// shortModelName — имя для роли: «openai/gpt-5.6-luna» → «gpt-5.6-luna».
// Приставка провайдера в списке ролей только мешает, а полное название
// модели остаётся в настройке и уходит в запрос.
func shortModelName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}

// connectAll подключает все модели провайдера разом: имя роли — короткое
// имя модели, полное уходит в запрос. Уже подключённые пропускаются.
func (s *Server) connectAll(cat catalogState) error {
	_, _, err := s.connectAllCounted(cat)
	return err
}

// connectAllCounted — то же самое, но числами: сколько подключено и
// сколько уже было. Обновление списка моделей собирает по ним свой итог и
// говорит заодно о том, чего у провайдера больше нет.
func (s *Server) connectAllCounted(cat catalogState) (int, int, error) {
	p, ok := s.eng.Provider(cat.Provider)
	if !ok {
		return 0, 0, fmt.Errorf("сначала выберите провайдера")
	}
	busy := map[string]bool{}
	for _, n := range s.eng.ProviderNames() {
		busy[n] = true
	}
	// ключ хранится один на провайдера: моделей у него бывают десятки, и
	// один и тот же ключ в каждой строке — лишняя копия секрета
	if strings.TrimSpace(cat.Key) != "" {
		if err := s.eng.SetProviderKey(p.Name, cat.Key); err != nil {
			return 0, 0, err
		}
	}
	added, skipped := 0, 0
	var last error
	for _, m := range cat.Models {
		name := shortModelName(m.ID)
		if busy[name] || busy[m.ID] {
			skipped++
			continue
		}
		err := s.eng.AddModel(engine.ModelConf{
			Name: name, Type: p.Kind, BaseURL: p.BaseURL, Model: m.ID,
			Vision: m.Vision, Hears: m.Hears, Context: m.Context, Price: catalogPrice(p, m),
			PriceBy: catalogPriceBy(p, m),
		})
		if err != nil {
			last, skipped = err, skipped+1
			continue
		}
		busy[name] = true
		added++
	}
	s.mu.Lock()
	s.catalog = catalogState{}
	s.lastCheck = fmt.Sprintf("%s: подключено моделей %d", cat.Provider, added)
	if skipped > 0 {
		s.lastCheck += fmt.Sprintf(" · пропущено %d (уже были или не приняты)", skipped)
	}
	if added == 0 && last != nil {
		s.lastCheck += " · " + last.Error()
	}
	// новые модели пришли без цен: их проверка идёт сразу, а не через сутки
	// в задаче, и итогом ложится карточка в «Уведомления»
	if added > 0 && (p.PriceKind == engine.PriceAPI || p.PriceKind == engine.PricePage) && s.eng.StartPriceCheck(p.Name) {
		s.lastCheck += " · цены проверяются: карточка придёт в «Уведомления»"
	}
	s.mu.Unlock()
	return added, skipped, nil
}

// catalogPrice — цена, с которой модель подключается. Каталог провайдера
// называет только цену входа, и она же становилась ценой выхода: выход
// почти везде дороже входа в несколько раз, и расход занижался. Там, где
// источник цен настроен, цену приносит проверка целиком (вход, кэш,
// выход) и предлагает человеку; каталог её не подставляет.
func catalogPrice(p engine.ProviderConf, m engine.CatalogModel) string {
	if p.PriceKind == engine.PriceAPI || p.PriceKind == engine.PricePage {
		return ""
	}
	return m.Price
}

// catalogPriceBy — кто назвал цену: то, что пришло от провайдера в списке
// моделей, — прайс, а не рука человека.
func catalogPriceBy(p engine.ProviderConf, m engine.CatalogModel) string {
	if catalogPrice(p, m) == "" {
		return ""
	}
	return engine.PriceByPrice
}
