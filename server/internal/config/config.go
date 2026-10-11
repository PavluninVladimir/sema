package config

import (
	"fmt"
	"os"
	"time"

	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Mail struct {
	Name             string `yaml:"name"`     // имя ящика в UI (по умолчанию = username)
	Auth             string `yaml:"auth"`     // "password" (по умолчанию) | "gmail" (OAuth)
	Backfill         string `yaml:"backfill"` // "full" — фоновый разбор всего ящика
	IMAPHost         string `yaml:"imap_host"`
	IMAPPort         int    `yaml:"imap_port"`
	Username         string `yaml:"username"`
	Password         string `yaml:"password"`
	PasswordEnv      string `yaml:"password_env"`
	JunkFolder       string `yaml:"junk_folder"`
	FetchWindowDays  int    `yaml:"fetch_window_days"`
	JunkRetentionDay int    `yaml:"junk_retention_days"`
	Google           Google `yaml:"google"`
}

// Google — OAuth-клиент типа Desktop из Google Cloud Console
// (APIs & Services → Credentials → Create OAuth client ID → Desktop app).
type Google struct {
	ClientID        string `yaml:"client_id"`
	ClientSecret    string `yaml:"client_secret"`
	ClientSecretEnv string `yaml:"client_secret_env"`
}

func (g *Google) Secret() string {
	if g.ClientSecret != "" {
		return g.ClientSecret
	}
	if g.ClientSecretEnv != "" {
		return os.Getenv(g.ClientSecretEnv)
	}
	return ""
}

func (m *Mail) IMAPPassword() string {
	if m.Password != "" {
		return m.Password
	}
	if m.PasswordEnv != "" {
		return os.Getenv(m.PasswordEnv)
	}
	return ""
}

// Provider описывает подключение к любой LLM. type: "openai" — любой
// OpenAI-совместимый сервер (LM Studio, Ollama, OpenRouter, vLLM...),
// type: "anthropic" — Claude API.
type Provider struct {
	Type      string `yaml:"type"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKey    string `yaml:"api_key"`
	APIKeyEnv string `yaml:"api_key_env"`
	// MaxOut — предел ответа в токенах. Agent API требует его для моделей
	// Anthropic и без него отвечает отказом; ноль — не передаём.
	MaxOut int `yaml:"max_out"`
}

func (p *Provider) Key() string {
	if p.APIKey != "" {
		return p.APIKey
	}
	if p.APIKeyEnv != "" {
		return os.Getenv(p.APIKeyEnv)
	}
	return ""
}

type LLM struct {
	Active    string              `yaml:"active"`
	Providers map[string]Provider `yaml:"providers"`
}

// Embeddings — OpenAI-совместимый /v1/embeddings; пустой base_url = RAG выключен.
type Embeddings struct {
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKey    string `yaml:"api_key"`
	APIKeyEnv string `yaml:"api_key_env"`
}

func (e *Embeddings) Key() string {
	if e.APIKey != "" {
		return e.APIKey
	}
	if e.APIKeyEnv != "" {
		return os.Getenv(e.APIKeyEnv)
	}
	return ""
}

type RAG struct {
	// Похожесть (косинус) на письмо, размеченное человеком, при которой
	// решение принимается без LLM: спам → авто-перенос, не спам → пропуск.
	AutoThreshold float64 `yaml:"auto_threshold"`
	Examples      int     `yaml:"examples"` // сколько размеченных примеров подмешивать в промпт
	AskTopK       int     `yaml:"ask_top_k"`
}

// Files — выкачивание вложений из писем.
type Files struct {
	Dir       string `yaml:"dir"`         // куда складывать (default "files")
	MaxSizeMB int    `yaml:"max_size_mb"` // вложения крупнее пропускаются
}

// Log — пределы файла журнала. Ноль означает значения по умолчанию:
// 10 МБ на файл, пять файлов.
type Log struct {
	LimitMB int `yaml:"limit_mb"`
	Keep    int `yaml:"keep"`
}

// TLS — где взять сертификат и на каком адресе слушать защищённо.
// Задаётся переменными SEMA_TLS_CERT, SEMA_TLS_KEY, SEMA_TLS_LISTEN:
// сертификат — файл на машине, а не запись в базе, потому что нужен он
// раньше, чем открывается база.
type TLS struct {
	Cert   string `yaml:"cert"`
	Key    string `yaml:"key"`
	Listen string `yaml:"listen"`
}

// DefaultTLSListen — адрес защищённого порта, пока его не назвали.
const DefaultTLSListen = "127.0.0.1:8443"

type Config struct {
	// Запуск: только то, что нельзя прочитать из базы раньше, чем она открыта
	Listen string `yaml:"listen"`
	Data   string `yaml:"data"`    // каталог: база, вложения, служебные файлы
	KeyEnv string `yaml:"key_env"` // имя переменной окружения с ключом шифрования
	// Журнал заводится до базы, поэтому его пределы живут здесь, а не в
	// настройках: когда база не открылась, писать причину уже надо
	Log Log `yaml:"log"`
	// TLS — защищённый порт рядом с обычным. Пусто — только обычный.
	TLS TLS `yaml:"tls"`

	// Ниже — первичное заполнение: при первом запуске переносится в базу и
	// дальше не читается. Настройки правятся формами, а не этим файлом.
	DB           string     `yaml:"db"`
	PollInterval string     `yaml:"poll_interval"`
	Mail         Mail       `yaml:"mail"`
	Extra        []Mail     `yaml:"accounts"` // дополнительные ящики (auth: password)
	LLM          LLM        `yaml:"llm"`
	Embeddings   Embeddings `yaml:"embeddings"`
	RAG          RAG        `yaml:"rag"`
	Files        Files      `yaml:"files"`

	// DataFrom — чем задан каталог данных: файл, переменная или аргумент.
	// Нужен, чтобы человек видел, какой источник победил: три места, и
	// молчаливое «победил кто-то» — это полдня поисков
	DataFrom string `yaml:"-"`

	// HasSeed — в файле есть что переносить в базу (ящик или модели)
	HasSeed bool   `yaml:"-"`
	Path    string `yaml:"-"` // откуда прочитан файл запуска
}

func (c *Config) PollEvery() time.Duration {
	d, err := time.ParseDuration(c.PollInterval)
	if err != nil || d < time.Minute {
		return 5 * time.Minute
	}
	return d
}

// Load читает файл запуска. Файла может не быть — тогда Сёма поднимается
// пустым и настраивается из приложения.
func Load(path string) (*Config, error) {
	c := &Config{
		Listen:       "127.0.0.1:8765",
		Data:         ".",
		KeyEnv:       "SEMA_KEY",
		PollInterval: "5m",
	}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		c.applyEnv()
		return c, c.fill()
	case err != nil:
		return nil, err
	}
	if err := yaml.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.HasSeed = c.Mail.Username != "" || len(c.LLM.Providers) > 0
	c.Path = path
	c.applyEnv()
	if err := c.fill(); err != nil {
		return nil, err
	}
	return c, nil
}

// applyEnv — переменные окружения важнее файла: так задают параметры в
// Docker и в systemd, где файла может не быть совсем.
func (c *Config) applyEnv() {
	if v := os.Getenv("SEMA_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("SEMA_DATA"); v != "" {
		c.SetDataDir(v, "переменная SEMA_DATA")
	}
	if v := os.Getenv("SEMA_KEY_ENV"); v != "" {
		c.KeyEnv = v
	}
	if v := os.Getenv("SEMA_TLS_CERT"); v != "" {
		c.TLS.Cert = v
	}
	if v := os.Getenv("SEMA_TLS_KEY"); v != "" {
		c.TLS.Key = v
	}
	if v := os.Getenv("SEMA_TLS_LISTEN"); v != "" {
		c.TLS.Listen = v
	}
}

// SetDataDir — каталог данных из источника выше файла (переменная
// или аргумент). Верхний источник перекрывает нижние **полностью**: без
// этого при явном `db:` в файле база оставалась по старому пути, а данные
// человек ждал в новом — и находил пустую установку.
func (c *Config) SetDataDir(path, source string) {
	if path == "" {
		return
	}
	c.Data, c.DataFrom = path, source
	c.DB = filepath.Join(path, "sema.db")
	c.Files.Dir = filepath.Join(path, "files")
}

// fill доводит значения до рабочих: пути внутри каталога данных, порты,
// имя ящика. Пустой конфиг — рабочее состояние, а не ошибка.
func (c *Config) fill() error {
	if c.Data == "" {
		// умолчание нативного запуска — это режим разработки, а не цель
		// установки: в контейнере каталог задан переменной
		c.Data, c.DataFrom = ".", "умолчание"
	}
	if c.DataFrom == "" {
		c.DataFrom = "файл запуска"
	}
	if c.KeyEnv == "" {
		c.KeyEnv = "SEMA_KEY"
	}
	// защищённый порт по умолчанию — только на петле, как и обычный: наружу
	// его открывает человек явно (SEMA_TLS_LISTEN или tls.listen), а не
	// умолчание. В контейнере петля своя — там адрес задаётся в примерах Docker
	if c.TLS.Listen == "" {
		c.TLS.Listen = DefaultTLSListen
	}
	// старые пути уважаем: у работающей установки раскладка не меняется
	if c.DB == "" {
		c.DB = filepath.Join(c.Data, "sema.db")
	}
	if c.Files.Dir == "" {
		c.Files.Dir = filepath.Join(c.Data, "files")
	}
	if c.RAG.AutoThreshold == 0 {
		c.RAG.AutoThreshold = 0.90
	}
	if c.RAG.Examples == 0 {
		c.RAG.Examples = 3
	}
	if c.RAG.AskTopK == 0 {
		c.RAG.AskTopK = 8
	}
	if c.Files.MaxSizeMB == 0 {
		c.Files.MaxSizeMB = 15
	}
	if c.Mail.Username == "" {
		return nil // ящик ещё не подключён — настроится из приложения
	}
	return c.fillMail()
}

func (c *Config) fillMail() error {
	if c.Mail.Auth == "" {
		c.Mail.Auth = "password"
	}
	if c.Mail.Auth == "gmail" {
		if c.Mail.IMAPHost == "" {
			c.Mail.IMAPHost = "imap.gmail.com"
		}
		if c.Mail.JunkFolder == "" {
			c.Mail.JunkFolder = "[Gmail]/Spam"
		}
		if c.Mail.Google.ClientID == "" {
			return fmt.Errorf("вход в Gmail требует mail.google.client_id")
		}
	}
	if c.Mail.IMAPPort == 0 {
		c.Mail.IMAPPort = 993
	}
	if c.Mail.JunkFolder == "" {
		c.Mail.JunkFolder = "Junk"
	}
	if c.Mail.FetchWindowDays == 0 {
		c.Mail.FetchWindowDays = 7
	}
	if c.Mail.JunkRetentionDay == 0 {
		c.Mail.JunkRetentionDay = 30
	}
	if c.Mail.IMAPHost == "" || c.Mail.Username == "" {
		return fmt.Errorf("для ящика нужны imap_host и username")
	}
	if c.Mail.Name == "" {
		c.Mail.Name = c.Mail.Username
	}
	for i := range c.Extra {
		if c.Extra[i].Name == "" {
			c.Extra[i].Name = c.Extra[i].Username
		}
		if c.Extra[i].IMAPPort == 0 {
			c.Extra[i].IMAPPort = 993
		}
		if c.Extra[i].FetchWindowDays == 0 {
			c.Extra[i].FetchWindowDays = c.Mail.FetchWindowDays
		}
		if c.Extra[i].JunkRetentionDay == 0 {
			c.Extra[i].JunkRetentionDay = c.Mail.JunkRetentionDay
		}
	}
	if c.RAG.AutoThreshold == 0 {
		c.RAG.AutoThreshold = 0.90
	}
	if c.RAG.Examples == 0 {
		c.RAG.Examples = 3
	}
	if c.RAG.AskTopK == 0 {
		c.RAG.AskTopK = 8
	}
	if c.Files.Dir == "" {
		c.Files.Dir = "files"
	}
	if c.Files.MaxSizeMB == 0 {
		c.Files.MaxSizeMB = 15
	}
	return nil
}

// KeyEnvName — имя переменной окружения с ключом шифрования секретов.
func (c *Config) KeyEnvName() string {
	if c.KeyEnv == "" {
		return "SEMA_KEY"
	}
	return c.KeyEnv
}
