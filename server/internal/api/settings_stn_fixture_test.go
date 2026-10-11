package api

// Данные для экранов «Хранилищ», «Скиллов» и «Уведомлений» (2 октября 2026):
// хранилища разных видов и состояний, файлы, наблюдаемые папки, скиллы всех
// источников с предложением правки, каналы доставки и два устройства с пушами.
// Значения секретов — приметные строки: проверка «не видно ни на одном экране»
// ищет их по тексту. Настоящих соединений нет: проверку хранилища подменяет
// `Server.checkStorageVia`, пушей не посылает никто.

import (
	"context"
	"testing"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/web"
)

// Значения, которых не должно быть ни в одной разметке.
const (
	stnAccessSentinel = "КЛЮЧ-ДОСТУПА-S3-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	stnSecretSentinel = "СЕКРЕТНЫЙ-КЛЮЧ-S3-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
	stnDiskSentinel   = "ТОКЕН-ДИСКА-ЗНАЧЕНИЕ-КОТОРОГО-НЕТ-НА-ЭКРАНЕ"
)

// Фраза, которая лежит в тексте скилла «backups» и больше нигде: если она есть в
// разметке списка, текст скилла опять едет в каждой строке.
const stnSkillPhrase = "ЭТА-ФРАЗА-ЛЕЖИТ-ТОЛЬКО-В-ТЕКСТЕ-СКИЛЛА-BACKUPS"

// stnFix — сервер с данными. Хранилища: местное; «yandex» — S3-зеркало, в которое
// Сёма пишет (последняя проверка — ошибка); «диск» — Яндекс.Диск, который остался
// вторым зеркалом, и «старый-ftp» — вид, которого Сёма не знает. Имена подобраны
// так, чтобы по порядку справочника зеркалом (первым не местным) оказался «yandex».
func stnFix(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st := testServer(t)
	for name, value := range map[string]string{"s3-access": stnAccessSentinel, "s3-secret": stnSecretSentinel, "disk-token": stnDiskSentinel} {
		if err := st.SaveSecret(&store.Secret{Name: name, Kind: store.SecretPassword}, value); err != nil {
			t.Fatal(err)
		}
	}
	for name, kind := range map[string]string{"yandex": "s3", "диск": "yadisk", "старый-ftp": "ftp"} {
		conf := "{}"
		switch kind {
		case "s3":
			conf = `{"endpoint":"storage.yandexcloud.net","bucket":"sema-files","region":"ru-central1","prefix":"sema","ssl":true,"access_secret":"s3-access","secret_secret":"s3-secret"}`
		case "yadisk":
			conf = `{"token_secret":"disk-token","folder":"Сёма"}`
		}
		if err := st.AddStorage(name, kind, conf); err != nil {
			t.Fatal(err)
		}
	}
	st.NoteStorage("yandex", "ошибка: dial tcp: lookup storage.yandexcloud.net: no such host")
	// файлы: три, один уже в зеркале
	var ids []int64
	for _, name := range []string{"счёт.pdf", "акт.pdf", "фото.jpg"} {
		att, _, err := s.eng.AddFile(context.Background(), name, "application/pdf", []byte("содержимое "+name),
			engine.FileSource{Kind: store.SourceUpload}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, att.ID)
	}
	if err := st.SetFileMirror(ids[0], "yandex"); err != nil {
		t.Fatal(err)
	}
	// папки: засеянная при первом запуске и ещё одна, которая не читается
	if err := st.AddFileFolder("/data/scans", true); err != nil {
		t.Fatal(err)
	}
	folders, _ := st.FileFolders()
	for _, f := range folders {
		if f.Path == "/data/scans" {
			st.NoteFileFolder(f.ID, "не читается: permission denied", 0)
		}
	}
	// скиллы: к встроенным — свой с фразой в тексте, из чата и предложение правки
	own := &store.Skill{Name: "backups", Title: "Резервные копии", Group: "Администрирование", Keywords: "бэкап, копия",
		When: "когда просят проверить резервные копии", Enabled: true, Source: store.SkillOwn,
		Text: "Проверь, что копия свежая.\n" + stnSkillPhrase + "\nСверь размер."}
	if err := s.eng.SaveSkill(own, "Вадим"); err != nil {
		t.Fatal(err)
	}
	fromChat := &store.Skill{Name: "chat-skill", Title: "Из чата", Group: "Прочее", When: "когда спрашивают про чат",
		Enabled: true, Source: store.SkillChat, Text: "Отвечай коротко."}
	if err := s.eng.SaveSkill(fromChat, "чат «Серверы»"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSkillDraft(&store.SkillDraft{ID: "d1", Name: "linux-admin", Title: "Linux-сервер", Kind: "chat",
		Who: "чат «Серверы»", Why: "в разговоре нашёлся урок", Diff: "+ проверять fail2ban", State: store.DraftOpen,
		Created: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	// уведомления: адрес входа https — пуши возможны; два устройства
	_ = st.SetSetting("auth.origin", "https://sema.example.org")
	if err := st.SaveNotifyChannel(&store.NotifyChannel{Kind: "web", Title: "Chrome на маке", Enabled: true,
		MinLevel: "warn", Sources: "servers,mail", QuietFrom: "23:00", QuietTo: "8:00"}); err != nil {
		t.Fatal(err)
	}
	subscribeAs(t, s, macChromeUA, "https://fcm.googleapis.com/fcm/send/M1", false, 0)
	subscribeAs(t, s, iphoneHomeUA, "https://web.push.apple.com/Q1", true, 5)
	phone, _ := st.PushSubscriptionByEndpoint("https://web.push.apple.com/Q1")
	if err := st.SetPushLevel(phone.ID, "info"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPushQuiet(phone.ID, "22:30-07:15"); err != nil {
		t.Fatal(err)
	}
	mac, _ := st.PushSubscriptionByEndpoint("https://fcm.googleapis.com/fcm/send/M1")
	st.MarkPushDelivery(mac.Endpoint, "", 201)
	return s, st
}

// stnText — весь текст экрана одной строкой: описание и разметка.
func stnText(t *testing.T, s *Server, screen string) string {
	t.Helper()
	doc, err := s.buildScreen(screen, "", "", 0)
	if err != nil {
		t.Fatalf("экран %s: %v", screen, err)
	}
	return dumpScreen(doc) + stnMarkup(t, doc)
}

// stnMarkup — разметка экрана так, как она уходит в браузер.
func stnMarkup(t *testing.T, doc *Document) string {
	t.Helper()
	m, err := web.Render(toScreen(doc))
	if err != nil {
		t.Fatal(err)
	}
	return string(m)
}

// stnVisibleButtons — сколько у строки видимых кнопок кроме тумблера и перехода: по
// ним считается «одно действие в строке».
func stnVisibleButtons(row *Component) int {
	n := 0
	for _, a := range row.Actions {
		if a.Label == "toggle" || a.Action == "$goto" {
			continue
		}
		n++
	}
	return n
}
