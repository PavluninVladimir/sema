package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/PavluninVladimir/sema/server/internal/script"
)

func (e *Engine) wechatSendTool(consumer string) script.Tool {
	return script.Tool{
		Name: "wechat.send", Need: "wechat", Args: "(peer, text, key)",
		Desc: "отправить текст в разрешённый чат WeChat из модуля с правом wechat. " +
			"Ключ одной отправки должен быть одинаковым при повторах, например ID входящего события. " +
			"Если результат неизвестен, проверьте чат перед новой отправкой",
		Fn: func(args []any) (any, error) {
			name, module := strings.CutPrefix(consumer, "module:")
			if !module || name == "" {
				return nil, errors.New("для отправки из скрипта нужен модуль с разрешением WeChat")
			}
			m, ok := e.Module(name)
			if !ok || !m.Enabled || !hasString(m.Needs, "wechat") {
				return nil, errors.New("модулю не разрешена отправка в WeChat")
			}
			peer, body, key := script.Str(args, 0), script.Str(args, 1), strings.TrimSpace(script.Str(args, 2))
			if key == "" || len(key) > 128 || strings.ContainsFunc(key, unicode.IsControl) {
				return nil, errors.New("укажите постоянный ключ этой отправки WeChat")
			}
			operation := sha256.Sum256([]byte(name + "\x00" + peer + "\x00" + key))
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			return nil, e.wechatSendText(ctx, peer, body, hex.EncodeToString(operation[:]))
		},
	}
}
