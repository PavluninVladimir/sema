package engine

// Ссылки на файлы (изменение file-share-links).
//
// 3 октября 2026 Сёма завёл в Planner баг со скриншотом из Telegram и не
// смог приложить картинку: у Planner нет загрузки файлов ни в MCP, ни в
// REST API. А у Сёмы есть публичный адрес — значит, можно дать ссылку на
// один файл: подпись от ключа из хранилища, срок, отзыв, журнал открытий.
// Секретное и файлы закрытых ящиков ссылкой не уходят никогда,
// чувствительное — только по действию человека.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/script"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	shareKeySecret = "share:key"  // ключ подписи ссылок: заводит ядро
	shareDaysKey   = "share.days" // срок ссылки по умолчанию
	CoreShare      = "share"      // потребитель ключа — ссылки на файлы
	// SharePath — путь ссылки: открыт без входа (openPath) и у шлюза.
	SharePath = "/share/"
)

var shareKeyMu sync.Mutex

// shareKey — ключ подписи; нет — заводится один раз.
func (e *Engine) shareKey() ([]byte, error) {
	shareKeyMu.Lock()
	defer shareKeyMu.Unlock()
	if v, ok := e.store.SecretValue(shareKeySecret); ok && v != "" {
		return []byte(v), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(raw)
	if err := e.store.SaveSecret(&store.Secret{Name: shareKeySecret, Kind: store.SecretToken,
		Note: "ключ подписи ссылок на файлы; смена гасит все ссылки разом", CreatedBy: store.SecretByCore}, key); err != nil {
		return nil, fmt.Errorf("ключ подписи ссылок не заведён: %w", err)
	}
	return []byte(key), nil
}

func shareSig(key []byte, share, file, exp int64) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%d.%d.%d", share, file, exp)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// ShareRequest — кто и откуда просит ссылку.
type ShareRequest struct {
	File    int64
	Days    int
	Who     string
	Origin  string
	Chat    int64
	ByHuman bool // нажал человек: только так ссылку получает «чувствительное»
}

// Share — выданная ссылка.
type Share struct {
	ID      int64
	URL     string
	Expires time.Time
	Name    string
}

func (e *Engine) shareDays() int { return e.store.SettingInt(shareDaysDef) }

// ShareFile — выдать ссылку на файл.
func (e *Engine) ShareFile(req ShareRequest) (*Share, error) {
	a, err := e.store.AttachmentByID(req.File)
	if err != nil || a == nil {
		return nil, fmt.Errorf("файла #%d нет", req.File)
	}
	if why := e.shareRefusal(a, req.ByHuman); why != "" {
		return nil, fmt.Errorf("%s", why)
	}
	base := strings.TrimRight(e.WebAddress(), "/")
	if base == "" {
		return nil, fmt.Errorf("у Сёмы не задан адрес входа (auth.origin): ссылку некуда вести")
	}
	key, err := e.shareKey()
	if err != nil {
		return nil, err
	}
	days := req.Days
	if days <= 0 {
		days = e.shareDays()
	}
	if days > 365 {
		days = 365
	}
	exp := time.Now().Add(time.Duration(days) * 24 * time.Hour).Truncate(time.Second)
	row := &store.FileShare{File: a.ID, Expires: exp.UTC().Format(time.RFC3339), Who: req.Who, Origin: req.Origin, Chat: req.Chat}
	if _, err := e.store.AddFileShare(row); err != nil {
		return nil, err
	}
	return &Share{ID: row.ID, URL: e.shareURL(base, key, row.ID, a.ID, exp.Unix()), Expires: exp, Name: a.Filename}, nil
}

func (e *Engine) shareURL(base string, key []byte, share, file, exp int64) string {
	return base + SharePath + strconv.FormatInt(share, 10) + "." + strconv.FormatInt(exp, 10) + "." + shareSig(key, share, file, exp)
}

// ShareURL — адрес уже выданной ссылки (для экрана файла): подпись
// вычисляется заново, хранить её незачем.
func (e *Engine) ShareURL(f *store.FileShare) string {
	key, err := e.shareKey()
	if err != nil {
		return ""
	}
	exp, err := time.Parse(time.RFC3339, f.Expires)
	if err != nil {
		return ""
	}
	return e.shareURL(strings.TrimRight(e.WebAddress(), "/"), key, f.ID, f.File, exp.Unix())
}

// shareRefusal — почему ссылки на этот файл не будет; пусто — можно.
func (e *Engine) shareRefusal(a *store.Attachment, byHuman bool) string {
	name := "«" + a.Filename + "»"
	class := e.ClassOfFile(a)
	if class == store.ClassSecret {
		return "файл " + name + " помечен «секретное» — ссылкой он не уходит никогда"
	}
	if closed := e.ClosedAccounts(true); len(closed) > 0 {
		for _, src := range e.store.FileSources(a.ID) {
			if src.Kind == store.SourceMail && closed[src.Account] {
				return "файл " + name + " из ящика «" + src.Account + "», закрытого для облачной модели, — ссылкой он не уходит"
			}
		}
	}
	if class == store.ClassSensitive && !byHuman && !e.ownersFile(a) {
		return "файл " + name + " чувствительный: ссылку на него выдаёт только человек — в строке файла на «Файлах»"
	}
	return ""
}

// ownersFile — файл прислал сам владелец: из админ-чата Telegram или
// загрузкой в Сёму. Такой «чувствительный» (на скриншоте нашёлся адрес
// почты) модель может выдать ссылкой сама: владелец сам его принёс, чтобы
// с ним работали, — как его голосовые уходят модели по правилу «свои».
// Секретное и закрытые ящики проверяются раньше и сюда не доходят.
func (e *Engine) ownersFile(a *store.Attachment) bool {
	for _, src := range e.store.FileSources(a.ID) {
		switch src.Kind {
		case store.SourceUpload:
			return true
		case store.SourceTG:
			parts := strings.Split(src.Ref, "/")
			if len(parts) < 2 {
				continue
			}
			id, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				continue
			}
			if c, ok := e.store.TGChat(parts[0], id); ok && c.Admin {
				return true
			}
		}
	}
	return false
}

// OpenShare — файл по ссылке. Любая неудача — одна и та же ошибка:
// подробности (была ли такая ссылка, вышел ли срок) наружу не говорятся.
func (e *Engine) OpenShare(token string) (*store.Attachment, *store.FileShare, error) {
	nope := fmt.Errorf("нет такой ссылки")
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil, nil, nope
	}
	id, err1 := strconv.ParseInt(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || id <= 0 {
		return nil, nil, nope
	}
	row, ok := e.store.FileShare(id)
	if !ok {
		return nil, nil, nope
	}
	key, err := e.shareKey()
	if err != nil {
		return nil, nil, nope
	}
	if !hmac.Equal([]byte(parts[2]), []byte(shareSig(key, id, row.File, exp))) {
		return nil, nil, nope
	}
	if t, err := time.Parse(time.RFC3339, row.Expires); err != nil || t.Unix() != exp || !row.Active(time.Now()) {
		return nil, nil, nope
	}
	a, err := e.store.AttachmentByID(row.File)
	if err != nil || a == nil {
		return nil, nil, nope
	}
	// класс мог подняться после выдачи: секретное и закрытый ящик — нет
	if why := e.shareRefusal(a, true); why != "" {
		return nil, nil, nope
	}
	e.store.NoteFileShareOpen(row.ID)
	return a, row, nil
}

// RevokeShare — ссылка больше не открывает файл.
func (e *Engine) RevokeShare(id int64) error {
	if _, ok := e.store.FileShare(id); !ok {
		return fmt.Errorf("ссылки %d нет", id)
	}
	return e.store.RevokeFileShare(id)
}

// ShareCard — карточка выданной ссылки в переписке: состояние берётся из
// базы, в сообщении — только номер.
type ShareCard struct {
	ID   int64  `json:"id"`
	File int64  `json:"file"`
	Name string `json:"name"`
}

// shareChatOf — чат, где выдают ссылку, по подписи потребителя: «chat:N» —
// сам чат, «assign:N» — чат поручения.
func (e *Engine) shareChatOf(consumer string) int64 {
	kind, rest, _ := strings.Cut(consumer, ":")
	id, _ := strconv.ParseInt(rest, 10, 64)
	switch kind {
	case "chat":
		return id
	case "assign":
		if a, ok := e.store.Assignment(id); ok {
			return a.Chat
		}
	}
	return 0
}

// shareTool — `sema.files.share(id, {days})`.
func (e *Engine) shareTool(consumer string) script.Tool {
	return script.Tool{
		Name: "files.share", Need: "files", Args: "(id, {days})",
		Desc: "ссылка на файл Сёмы для чужой системы (задача Planner, письмо): {url, expires, id}. Открывается без входа до срока " +
			"(по умолчанию из настройки share.days), отзывается на экране файла и в карточке. Секретное, файлы закрытых ящиков и " +
			"чувствительное ссылкой не уходят. В заметку задачи Planner (note — Quill Delta) ставь ссылку текстом: " +
			`[{"insert":"Скриншот: "},{"insert":"<url>","attributes":{"link":"<url>"}},{"insert":"\n"}]` +
			" — картинку по чужому адресу Planner не показывает («не удалось загрузить файл»), только ссылку",
		Fn: func(a []any) (any, error) {
			id, err := strconv.ParseInt(strings.TrimPrefix(script.Str(a, 0), "#"), 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("нужен номер файла")
			}
			days := 0
			if v, ok := script.Opts(a, 1)["days"].(float64); ok {
				days = int(v)
			}
			chat := e.shareChatOf(consumer)
			who := "модель"
			if strings.HasPrefix(consumer, "module:") {
				who = "модуль «" + strings.TrimPrefix(consumer, "module:") + "»"
			}
			sh, err := e.ShareFile(ShareRequest{File: id, Days: days, Who: who, Origin: consumer, Chat: chat})
			if err != nil {
				return nil, err
			}
			if chat > 0 {
				_ = e.saveToolCard(chat, ToolCard{Title: "Ссылка на файл", Share: &ShareCard{ID: sh.ID, File: id, Name: sh.Name}})
			}
			return map[string]any{"url": sh.URL, "expires": sh.Expires.Format(time.RFC3339), "id": sh.ID}, nil
		},
	}
}

// ShareFromScreen — «Выдать ссылку» на экране файла: решение человека.
func (e *Engine) ShareFromScreen(ctx context.Context, file int64) (*Share, error) {
	return e.ShareFile(ShareRequest{File: file, Who: "вы", Origin: "экран файла", ByHuman: true})
}
