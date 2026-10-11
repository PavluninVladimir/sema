package engine

// Модель эмбеддингов выбирается в настройках: список берётся с самого
// сервера эмбеддингов — что там поднято, то и предлагаем. У разных моделей
// векторы несовместимы, поэтому после смены индекс пересчитывается.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/embed"
)

const (
	embedModelKey = "embed_model"
	embedURLKey   = "embed_base_url"
)

func (e *Engine) EmbedModel() string {
	if m := e.Role(RoleEmbed).Model; m != "" && strings.Contains(m, "embed") {
		return m
	}
	return e.store.Setting(embedModelKey, e.conf.Embeddings.Model)
}

func (e *Engine) EmbedBaseURL() string {
	return e.store.Setting(embedURLKey, e.conf.Embeddings.BaseURL)
}

// embedModelsCache — что сервер эмбеддингов отвечал в последний раз. Список
// нужен экрану «Модели и роли → Роли», а он первый на странице раздела:
// сервер, который не отвечает, держал бы её до пяти секунд при каждом
// открытии. Минуты хватает — поднятые модели меняются раз в год; смена адреса
// или модели сбрасывает запомненное (SetEmbeddings).
var embedModelsCache struct {
	sync.Mutex
	base string
	at   time.Time
	list []string
}

const embedModelsTTL = time.Minute

// EmbedModels — модели эмбеддингов, поднятые на сервере.
func (e *Engine) EmbedModels() []string {
	base := strings.TrimRight(hostAlias(e.EmbedBaseURL()), "/")
	if base == "" {
		return nil
	}
	embedModelsCache.Lock()
	if embedModelsCache.base == base && time.Since(embedModelsCache.at) < embedModelsTTL {
		list := append([]string(nil), embedModelsCache.list...)
		embedModelsCache.Unlock()
		// выбранная сейчас модель в списке есть всегда: иначе выпадашка
		// молча подменила бы её первой попавшейся
		return withCurrent(list, e.EmbedModel())
	}
	embedModelsCache.Unlock()
	list := e.askEmbedModels(base)
	embedModelsCache.Lock()
	embedModelsCache.base, embedModelsCache.at, embedModelsCache.list = base, time.Now(), append([]string(nil), list...)
	embedModelsCache.Unlock()
	return list
}

// EmbedModelsCached — то, что сервер эмбеддингов отдавал в прошлый раз, без
// обращения к нему: сроком давности запомненное не проверяется, а запомнено
// ничего — остаётся одна выбранная модель. Нужен тем, кому ходить по сети нельзя
// (индекс настроек собирает экраны, ничего не делая наружу).
func (e *Engine) EmbedModelsCached() []string {
	base := strings.TrimRight(hostAlias(e.EmbedBaseURL()), "/")
	embedModelsCache.Lock()
	var list []string
	if base != "" && embedModelsCache.base == base {
		list = append(list, embedModelsCache.list...)
	}
	embedModelsCache.Unlock()
	return withCurrent(list, e.EmbedModel())
}

// withCurrent — список, в котором есть выбранное значение.
func withCurrent(list []string, current string) []string {
	for _, v := range list {
		if v == current {
			return list
		}
	}
	return append([]string{current}, list...)
}

// forgetEmbedModels — запомненный список устарел: адрес или модель сменились.
func forgetEmbedModels() {
	embedModelsCache.Lock()
	embedModelsCache.base = ""
	embedModelsCache.Unlock()
}

// askEmbedModels — спросить у сервера, что на нём поднято.
func (e *Engine) askEmbedModels(base string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{e.EmbedModel()}
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return []string{e.EmbedModel()}
	}
	var models []string
	for _, m := range out.Data {
		if strings.Contains(strings.ToLower(m.ID), "embed") {
			models = append(models, m.ID)
		}
	}
	if len(models) == 0 {
		models = []string{e.EmbedModel()}
	}
	return models
}

// embedTrustKey — доверие эмбеддеру: как у моделей, утверждение человека.
// Вектор считается по содержимому письма, и секретное недоверенному
// эмбеддеру не отдаётся. Разовый перенос: пока признака не было, «дома»
// считалось по адресу — при первом старте это переносится в настройку.
const embedTrustKey = "embed.trusted"

// EmbedTrusted — можно ли эмбеддеру закрытое и секретное.
func (e *Engine) EmbedTrusted() bool {
	return e.store.Setting(embedTrustKey, "") == "true"
}

// SetEmbedTrust — доверять эмбеддеру или считать внешним.
func (e *Engine) SetEmbedTrust(on bool) error {
	v := "false"
	if on {
		v = "true"
	}
	return e.store.SetSetting(embedTrustKey, v)
}

func (e *Engine) seedEmbedTrust() {
	if e.store.Setting(embedTrustKey, "") != "" || e.embedder == nil {
		return
	}
	v := "false"
	if !e.embedder.Outside {
		v = "true"
	}
	_ = e.store.SetSetting(embedTrustKey, v)
}

// SetEmbeddings меняет модель и адрес сервера. Если модель другая — старые
// векторы больше не сравнимы с новыми, и индекс считается заново.
func (e *Engine) SetEmbeddings(model, baseURL string) (bool, error) {
	changed := model != e.EmbedModel()
	_ = e.SetRoleField(RoleEmbed, "model", model)
	if err := e.store.SetSetting(embedModelKey, model); err != nil {
		return false, err
	}
	if err := e.store.SetSetting(embedURLKey, baseURL); err != nil {
		return false, err
	}
	forgetEmbedModels()
	if baseURL != "" {
		e.embedder = embed.New(hostAlias(baseURL), model, e.conf.Embeddings.Key())
	} else {
		e.embedder = nil
	}
	if changed {
		if err := e.store.ClearEmbeddings(); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// EmbedState — что сейчас с индексом: сколько посчитано и сколько ждёт.
func (e *Engine) EmbedState() (done, waiting int, note string) {
	done, waiting = e.store.EmbeddingCounts()
	switch {
	case e.embedder == nil:
		note = "выключены: не задан сервер эмбеддингов"
	case waiting > 0:
		note = fmt.Sprintf("считаются фоновой задачей · осталось %d", waiting)
	default:
		note = "актуально"
	}
	return done, waiting, note
}

// AskTopK, RagExamples и FilesMaxMB — перенесённые из файла настройки:
// теперь они живут в базе и правятся формой.
func (e *Engine) AskTopK() int { return e.intSetting("rag.ask_top_k", e.conf.RAG.AskTopK, 8) }

func (e *Engine) RagExamples() int { return e.intSetting("rag.examples", e.conf.RAG.Examples, 3) }

func (e *Engine) FilesMaxMB() int { return e.intSetting("files.max_mb", e.conf.Files.MaxSizeMB, 15) }

func (e *Engine) SetIntSetting(key string, v int) error {
	return e.store.SetSetting(key, strconv.Itoa(v))
}

func (e *Engine) intSetting(key string, fromFile, def int) int {
	if v, err := strconv.Atoi(e.store.Setting(key, "")); err == nil && v > 0 {
		return v
	}
	if fromFile > 0 {
		return fromFile
	}
	return def
}
