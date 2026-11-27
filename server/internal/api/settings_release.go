package api

import (
	"net/url"
	"os"
	"path/filepath"

	"github.com/PavluninVladimir/sema/server/internal/version"
)

func (s *Server) buildReleaseSettings(doc *Document) {
	doc.Subheading = "Обновления из GitHub Releases"
	endpoint := Component{Type: "row", Layout: "wide", Subtitle: "URL webhook", Caption: "Укажите публичный HTTPS-адрес Сёмы во «Входе и доступе»."}
	if origin, err := url.Parse(s.auth.Origin()); err == nil && origin.Scheme == "https" && origin.Host != "" && origin.User == nil {
		endpoint.Caption = "https://" + origin.Host + releaseWebhookPath
		endpoint.Actions = []Action{{Action: "$copy", Target: endpoint.Caption, Label: "Скопировать URL"}}
	}
	state := Component{Type: "row", Layout: "wide", Subtitle: "Немедленная проверка", Caption: "Готова к запуску по webhook", Level: LevelOK}
	signalDir, err := os.Stat(filepath.Join(s.eng.DataDir(), releaseSignalDir))
	switch {
	case len(os.Getenv("SEMA_RELEASE_WEBHOOK_SECRET")) < 32:
		state.Caption, state.Level = "Нужен ключ подписи на сервере Сёмы и в GitHub Actions", LevelWarn
	case !s.eng.Store().SettingFlag(releaseImmediate):
		state.Caption, state.Level = "Выключена. Действует расписание на хосте.", ""
	case err != nil || !signalDir.IsDir():
		state.Caption, state.Level = "Ожидает настройки службы обновления на хосте", LevelWarn
	}
	doc.Components = append(doc.Components,
		Component{Type: "section", ID: "release-webhook", Title: "Webhook GitHub", Children: []Component{
			endpoint, state,
			{Type: "row", Layout: "wide", Subtitle: "Репозиторий", Caption: releaseRepository()},
			{Type: "row", Layout: "wide", Subtitle: "Установленная сборка", Caption: version.String()},
		}},
		Component{Type: "form", ID: "release-update", Style: formRows, Fields: []Field{
			s.settingField(releaseImmediate.Key, "immediate", "Проверять обновление сразу по webhook",
				"Подписанный сигнал GitHub запускает проверку пакета. Резервная проверка выполняется по расписанию на хосте.", ""),
		}, Submit: &Action{Action: "set_release_webhook", Label: "Сохранить"}},
	)
}
