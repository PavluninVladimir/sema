package engine

// Разовый перенос настроек из файла запуска в базу. После него файл в этой
// части не читается: единственный источник истины — база, а правятся
// настройки формами. Тот же механизм позволяет поднять Сёму на новой машине
// с готовым файлом-заготовкой и потом этот файл удалить.

import (
	"fmt"
	"log"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const seedDoneKey = "config_imported"

// importSeed переносит то, что есть в файле. Повторный запуск ничего не
// перезаписывает: перенос помечается флагом.
func (e *Engine) importSeed(conf *config.Config) {
	if !conf.HasSeed || e.store.Setting(seedDoneKey, "") == "1" {
		return
	}
	moved := 0

	// ящики: параметры разбора и доступ переезжают в настройки ящика
	for _, acct := range append([]config.Mail{conf.Mail}, conf.Extra...) {
		if acct.Username == "" {
			continue
		}
		c := e.store.MailConf(acct.Name)
		if c.Auth == "" {
			switch acct.Auth {
			case "gmail":
				c.Auth = store.AuthGoogle
			default:
				c.Auth = store.AuthAppPassword
			}
		}
		if c.Host == "" {
			c.Host = acct.IMAPHost
		}
		if c.Port == 0 {
			c.Port = acct.IMAPPort
		}
		if c.Username == "" {
			c.Username = acct.Username
		}
		if c.Password == "" {
			c.Password = acct.IMAPPassword()
		}
		if c.ClientID == "" {
			c.ClientID = acct.Google.ClientID
			c.Secret = acct.Google.Secret()
		}
		if c.FetchDays == 0 {
			c.FetchDays = acct.FetchWindowDays
		}
		if c.JunkDays == 0 {
			c.JunkDays = acct.JunkRetentionDay
		}
		if c.Junk == "" {
			c.Junk = acct.JunkFolder
		}
		if err := e.store.SaveMailConf(acct.Name, c); err != nil {
			log.Printf("перенос настроек ящика %s: %v", acct.Name, err)
			continue
		}
		moved++
	}

	// модели: имя, адрес и ключ переезжают, чтобы файл можно было удалить
	existing := map[string]bool{}
	for _, m := range e.userModels() {
		existing[m.Name] = true
	}
	var models []ModelConf
	for name, p := range conf.LLM.Providers {
		if existing[name] {
			continue
		}
		models = append(models, ModelConf{
			Name: name, Type: p.Type, BaseURL: p.BaseURL, Model: p.Model, APIKey: p.Key(),
		})
	}
	if len(models) > 0 {
		if err := e.saveUserModels(append(e.userModels(), models...)); err != nil {
			log.Printf("перенос моделей: %v", err)
		} else {
			moved += len(models)
		}
	}

	// пороги разбора, вложения, эмбеддинги
	seedSetting := func(key, value string) {
		if value != "" && e.store.Setting(key, "") == "" {
			_ = e.store.SetSetting(key, value)
			moved++
		}
	}
	seedSetting("rag.examples", fmt.Sprint(conf.RAG.Examples))
	seedSetting("rag.ask_top_k", fmt.Sprint(conf.RAG.AskTopK))
	seedSetting("files.max_mb", fmt.Sprint(conf.Files.MaxSizeMB))
	seedSetting(embedURLKey, conf.Embeddings.BaseURL)
	seedSetting(embedModelKey, conf.Embeddings.Model)
	if conf.LLM.Active != "" {
		seedSetting("llm.active", conf.LLM.Active)
	}

	_ = e.store.SetSetting(seedDoneKey, "1")
	log.Printf("настройки из файла перенесены в базу: записей %d · дальше файл в этой части не читается", moved)
	if secrets := seedSecretsInFile(conf); len(secrets) > 0 {
		log.Printf("в файле остались секреты (%s) — они уже в базе, файл можно удалить",
			strings.Join(secrets, ", "))
	}
}

// seedSecretsInFile — что из секретов лежит прямо в файле открытым текстом.
func seedSecretsInFile(conf *config.Config) []string {
	var out []string
	if conf.Mail.Google.ClientSecret != "" {
		out = append(out, "секрет OAuth")
	}
	if conf.Mail.Password != "" {
		out = append(out, "пароль ящика")
	}
	for name, p := range conf.LLM.Providers {
		if p.APIKey != "" {
			out = append(out, "ключ модели "+name)
		}
	}
	return out
}

// SecretsInStartupFile — что из секретов ещё лежит в файле запуска.
func (e *Engine) SecretsInStartupFile() []string { return seedSecretsInFile(e.conf) }

// StartupFilePath — где лежит файл запуска (для подсказки в настройках).
func (e *Engine) StartupFilePath() string { return e.conf.Path }

// KeyEnvName — имя переменной с ключом шифрования, для подсказок в UI.
func (e *Engine) KeyEnvName() string { return e.conf.KeyEnvName() }
