package engine

// Каталог моделей: список берём у самого провайдера. Где провайдер отдаёт
// возможности (OpenRouter), показываем их рядом с именем — иначе человек
// выбирает модель вслепую и ставит читать картинки ту, что их не видит.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type CatalogModel struct {
	ID      string
	Vision  bool
	Hears   bool // слышит: распознаёт речь (по названию)
	Context int
	Price   string // за миллион входных токенов, как отдал провайдер
}

// Title — строка для списка: имя и то, что о модели известно.
func (m CatalogModel) Title() string {
	var parts []string
	if m.Vision {
		parts = append(parts, "видит картинки")
	}
	if m.Hears {
		parts = append(parts, "слышит речь")
	}
	if m.Context > 0 {
		parts = append(parts, humanContext(m.Context))
	}
	if m.Price != "" {
		parts = append(parts, m.Price)
	}
	if len(parts) == 0 {
		return m.ID
	}
	return m.ID + " · " + strings.Join(parts, " · ")
}

func humanContext(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.Itoa(n/1_000_000) + "M контекст"
	case n >= 1000:
		return strconv.Itoa(n/1000) + "k контекст"
	default:
		return strconv.Itoa(n) + " контекст"
	}
}

// Catalog спрашивает у провайдера список моделей.
func (e *Engine) Catalog(ctx context.Context, p ProviderConf, key string) ([]CatalogModel, error) {
	if p.Kind == "cli" {
		return nil, fmt.Errorf("клиент по подписке сам выбирает модель")
	}
	if !p.Catalog || p.BaseURL == "" {
		return nil, fmt.Errorf("этот провайдер не отдаёт список — впишите название модели")
	}
	base := strings.TrimRight(p.BaseURL, "/")
	url := base + "/models"
	switch p.Kind {
	case "anthropic":
		url = base + "/v1/models"
	case "perplexity":
		// Agent API держит свой список отдельно от Router API
		url = strings.TrimSuffix(base, "/v1") + "/v1/models"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	switch p.Kind {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не отвечает: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("ключ не подошёл")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ответил кодом %d", resp.StatusCode)
	}

	var out struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			Architecture  struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt string `json:"prompt"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("непонятный ответ: %w", err)
	}
	models := make([]CatalogModel, 0, len(out.Data))
	for _, m := range out.Data {
		cm := CatalogModel{ID: m.ID, Context: m.ContextLength, Hears: hearsByName(m.ID)}
		for _, mod := range m.Architecture.InputModalities {
			if mod == "image" {
				cm.Vision = true
			}
		}
		cm.Price = pricePerMillion(m.Pricing.Prompt)
		models = append(models, cm)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("список пуст")
	}
	return models, nil
}

// pricePerMillion переводит цену за токен в цену за миллион — так её
// сравнивают глазами.
func pricePerMillion(raw string) string {
	if raw == "" {
		return ""
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return ""
	}
	return fmt.Sprintf("$%.2f / 1M", v*1_000_000)
}
