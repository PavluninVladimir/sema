package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

func TestWeChatForwardText(t *testing.T) {
	for _, test := range []struct {
		name, source, text, reply, want string
		calls                           int
		wantError                       bool
	}{
		{"original", WeChatForwardOriginal, "你好\n", "", "你好\n", 0, false},
		{"Russian text", "Китайский", "Увидимся завтра.", "", "Увидимся завтра.", 0, false},
		{"Chinese text", "Китайский", "你好", "{\"text\":\"Привет\"}", "Привет", 1, false},
		{"mixed", "Китайский", "你好, Алексей", "{\"text\":\"Привет, Алексей\"}", "Привет, Алексей", 1, false},
		{"Japanese text", "Китайский", "昨日はありがとう", "{\"text\":null}", "昨日はありがとう", 1, false},
		{"invalid translation", WeChatForwardAny, "hello", "missing JSON", "", 1, true},
		{"missing text field", WeChatForwardAny, "hello", "{}", "", 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := store.WeChatForwardMessage{Body: test.text, SourceLang: test.source, TargetLang: "Русский"}
			var calls int
			got, err := wechatForwardText(context.Background(), m, func(_ context.Context, prompt string) (string, error) {
				calls++
				if !strings.Contains(prompt, "\"target_language\":\"Русский\"") {
					t.Errorf("target language missing from prompt: %q", prompt)
				}
				return test.reply, nil
			})
			if got != test.want || (err != nil) != test.wantError || calls != test.calls {
				t.Fatalf("got %q, err=%v, calls=%d", got, err, calls)
			}
		})
	}
	ctx := context.Background()
	m := store.WeChatForwardMessage{Body: "你好", SourceLang: "Китайский", TargetLang: "Русский"}
	modelOffline := errors.New("model offline")
	out, err := wechatForwardText(ctx, m, func(context.Context, string) (string, error) { return "", modelOffline })
	if !errors.Is(err, modelOffline) || out != "" {
		t.Fatalf("expected model error without sending an untranslated fallback, got %q, %v", out, err)
	}
}
