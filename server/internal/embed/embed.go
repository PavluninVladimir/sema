// Package embed — векторизация текста через любой OpenAI-совместимый
// /v1/embeddings (LM Studio, Ollama, OpenAI...) и косинусный поиск.
// Векторы лежат в SQLite; для личной почты перебор быстрее и проще
// любой векторной БД, в том числе на Raspberry Pi.
package embed

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/llm"
	"time"
)

type Embedder struct {
	BaseURL string
	Model   string
	APIKey  string
	// Outside — эмбеддер стоит не дома. Считается по адресу, а не
	// настройкой: человек не должен помнить, что поменял base_url и тем
	// самым открыл дверь наружу. Секретное такому не отдаётся.
	Outside bool
	client  *http.Client
}

func New(baseURL, model, apiKey string) *Embedder {
	return &Embedder{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		APIKey:  apiKey,
		Outside: isRemote(baseURL),
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: HTTP %d: %s", resp.StatusCode, raw[:min(len(raw), 300)])
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: want %d vectors, got %d", len(texts), len(out.Data))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

func (e *Embedder) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func EncodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func DecodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// isRemote — уходит ли этот адрес из своей сети. Правило общее с моделями:
// см. llm.HostIsHome.
func isRemote(baseURL string) bool { return !llm.HostIsHome(baseURL) }
