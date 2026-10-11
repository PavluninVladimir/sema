package api

// Экран одного хранилища: `settings:storages@<имя>`. Шапку (путь назад,
// закреплённая строка) рисует общий entityFrame, содержимое — здесь:
//
//   - что не так — вверху (не названы ключи, нет секрета);
//   - итог последней проверки и её ошибка: в строке списка про них остаётся одна
//     капсула, а подробности живут здесь;
//   - что это за хранилище и сколько в нём файлов: сколько лежит, сколько ждёт
//     зеркала;
//   - параметры полями строками настройки — их сохраняет нижняя полоса; ключи
//     названы именем секрета со ссылкой на сам секрет, значений ключей здесь нет;
//   - «Опасное» внизу своей карточкой: убрать хранилище, с вопросом. Местное
//     хранилище не убирается: из него Сёма читает файлы.
//
// Решений экран не принимает: проверка, добавление и удаление — прежние действия
// (`check_storage`, `delete_storage`), а сохранение полей — `save_storage`, которое
// заменило вопрос «Токен Диска» (прежний `set_yadisk_token` работает как раньше).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/storage"
)

// storageCaption — одна строка фактов закреплённой шапки: вид и куда смотрит.
func storageCaption(i storageInfo) string {
	switch {
	case !i.Known:
		return "тип «" + i.Conf.Kind + "» не поддерживается"
	case i.Local:
		return "местное · " + i.Where
	}
	return i.Where
}

func (s *Server) buildStorage(doc *Document, a storagesAddr) error {
	doc.Layout = "read"
	c, ok := s.storageByName(a.Name)
	if !ok {
		// хранилище убрали, пока экран был открыт: не ошибка, а объяснение
		doc.Heading = "Хранилища"
		doc.Components = append(doc.Components,
			Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: storagesScreen, Label: "‹ Хранилища"}}},
			emptyState("Хранилища «"+a.Name+"» нет", "Его убрали или переименовали. Список — по кнопке «‹ Хранилища» выше", "externaldrive"))
		return nil
	}
	infos := s.storageInfos()
	var info storageInfo
	var names []string
	for _, i := range infos {
		names = append(names, i.Conf.Name)
		if i.Conf.Name == c.Name {
			info = i
		}
	}
	sort.Strings(names)
	add := func(cmp Component) { doc.Components = append(doc.Components, cmp) }

	s.storageProblem(add, info)
	s.storageCheckRow(add, info)
	s.storageFacts(add, info)
	s.storageParams(add, info)
	if info.Known && !info.Local {
		s.storageKeys(add, info)
	}
	switch {
	case info.Local:
		add(Component{Type: "section", ID: "storage-local-note", Children: []Component{{
			Type: "row", Layout: "wide", ID: "storage-undeletable", Subtitle: "Не убирается",
			Caption: "Местное хранилище — основное: из него Сёма читает файлы. Убрать можно только зеркало; папка задаётся при запуске сервера, отсюда не меняется"}}})
	default:
		add(Component{Type: "section", ID: "storage-danger", Title: "Опасное", Style: "danger", Children: []Component{{
			Type: "row", Layout: "wide", ID: "danger-remove",
			Subtitle: "Убрать хранилище",
			Caption:  "Файлы там остаются, Сёма перестаёт туда писать и переучитывает свои копии: что было отмечено «в зеркале», снова ждёт зеркала",
			Actions:  []Action{removeStorageAction(c.Name)}}}})
	}

	letter, color := storageTile(c)
	e := entity{Section: storagesScreen, Back: "Хранилища", ID: c.Name, Name: c.Name,
		Avatar: letter, AvatarColor: color, Caption: storageCaption(info), Switch: "Хранилище"}
	e.Badges = []Badge{info.pill()}
	if role := info.role(); role != "" {
		e.Badges = append(e.Badges, Badge{Text: role})
	}
	e.Level = info.level()
	if info.Known {
		e.Primary = &Action{Action: "check_storage", Target: c.Name, Label: "Проверить"}
	}
	e.Others = names
	entityFrame(doc, e)
	return nil
}

// storageProblem — что мешает хранилищу работать: стоит вверху, как ждущее
// решения. Нет секрета — действие ведёт заводить его, а не просто сообщает.
func (s *Server) storageProblem(add func(Component), i storageInfo) {
	if i.Problem == "" {
		return
	}
	row := Component{Type: "row", Layout: "wide", ID: "storage-problem", Level: LevelWarn, Dot: levelDot(LevelWarn),
		Subtitle: "Хранилище не заработает: " + i.Problem}
	missing := ""
	switch i.Conf.Kind {
	case "s3":
		var conf storage.S3Config
		_ = json.Unmarshal([]byte(i.Conf.Config), &conf)
		for _, name := range []string{conf.AccessSecret, conf.SecretSecret} {
			if name != "" && !s.vaultHas(name) {
				missing = name
				break
			}
		}
	case "yadisk", "webdav":
		var conf engine.YaDiskConf
		_ = json.Unmarshal([]byte(i.Conf.Config), &conf)
		if conf.Token != "" && !s.vaultHas(conf.Token) {
			missing = conf.Token
		}
	}
	if missing != "" {
		row.Caption = "Заведите секрет с таким именем — или выберите другой в параметрах ниже"
		row.Actions = []Action{{Action: "$goto", Target: entityAddr(secretsScreen, secretsNew, secretsNewName+missing),
			Label: "Завести секрет", Style: "primary"}}
	} else {
		row.Caption = "Выберите секреты в параметрах ниже и нажмите «Сохранить»"
	}
	add(Component{Type: "section", ID: "storage-alerts", Children: []Component{row}})
}

// vaultHas — есть ли секрет с таким именем в «Ключах и секретах».
func (s *Server) vaultHas(name string) bool {
	secrets, _ := s.eng.Store().VaultSecrets()
	for _, sec := range secrets {
		if sec.Name == name {
			return true
		}
	}
	return false
}

// storageCheckRow — итог последней проверки: что она сказала и когда. Ошибка
// проверки живёт здесь, а не в строке списка: там про неё одна капсула.
func (s *Server) storageCheckRow(add func(Component), i storageInfo) {
	if !i.Known || i.Local {
		return
	}
	s.mu.Lock()
	last := s.lastStorageCheck
	s.mu.Unlock()
	// то, что сказала проверка в этом запуске, свежее записанного в хранилище:
	// отказ «не умеет рассказать о себе» в запись не попадает
	said, own := strings.CutPrefix(last, i.Conf.Name+": ")
	if !own || said == "" {
		said = i.Note
	}
	row := Component{Type: "row", Layout: "wide", ID: "storage-check", Subtitle: "Последняя проверка"}
	switch {
	case strings.HasPrefix(said, "ошибка"):
		row.Subtitle = "Последняя проверка · не отвечает"
		row.Caption = strings.TrimPrefix(said, "ошибка: ")
		withLevel(&row, LevelAlert)
	case said != "":
		row.Caption = said
		withLevel(&row, LevelOK)
	default:
		row.Caption = "ещё не проверялось: «Проверить» соединится, создаст bucket, если его нет, и проверит запись и чтение пробным файлом"
		if i.Conf.Kind == "yadisk" || i.Conf.Kind == "webdav" {
			row.Caption = "ещё не проверялось: «Проверить» спросит у Диска, кто он, и сверит размер и sha256 пробного файла"
		}
	}
	add(Component{Type: "section", ID: "storage-check-card", Children: []Component{row}})
}

// storageFacts — что это за хранилище и сколько в нём файлов.
func (s *Server) storageFacts(add func(Component), i storageInfo) {
	total, bytes, _ := s.eng.Store().FileStats()
	var rows []Component
	switch {
	case !i.Known:
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "storage-kind", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: "Тип «" + i.Conf.Kind + "» Сёме незнаком",
			Caption:  "Он не умеет проверять такое хранилище и писать в него. Запись справочника осталась от прежних версий: её можно убрать"})
	case i.Local:
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "storage-role", Subtitle: "Основное",
			Caption: "Файлы читаются отсюда; одинаковые лежат один раз, по хэшу содержимого",
			Facts:   []Prop{{Label: "Папка", Value: s.eng.FilesDir()}}})
		files := Component{Type: "row", Layout: "wide", ID: "storage-files", Subtitle: "Файлы"}
		if i.StatOK {
			files.Facts = []Prop{{Label: "Лежит", Value: plural(i.Files, "файл", "файла", "файлов")},
				{Label: "Занято", Value: humanBytes(i.Bytes)}}
		} else {
			files.Caption = "посчитать не вышло"
		}
		rows = append(rows, files)
	case i.Mirror:
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "storage-role", Subtitle: "Зеркало",
			Caption: "Копия каждого файла уезжает сюда. Читается файл всё равно с диска, а отсюда — только если местная копия потерялась",
			Facts:   []Prop{{Label: "Куда смотрит", Value: i.Where}}})
		files := Component{Type: "row", Layout: "wide", ID: "storage-files", Subtitle: "Файлы",
			Facts: []Prop{
				{Label: "Лежит в зеркале", Value: fmt.Sprintf("%d из %d", i.Done, i.All)},
				{Label: "Ждёт зеркала", Value: fmt.Sprint(i.All - i.Done)},
				{Label: "Всего у Сёмы", Value: plural(total, "файл", "файла", "файлов") + ", " + humanBytes(bytes)}}}
		if i.All-i.Done > 0 {
			withLevel(&files, LevelWarn)
			if !s.eng.MirrorOn() {
				files.Caption = "Зеркало выключено — файлы не уезжают. Включается на вкладке «Откуда берутся»"
				files.Actions = []Action{{Action: "$goto", Target: entityAddr(storagesScreen, "", storTabSources), Label: "Откуда берутся"}}
			}
		}
		rows = append(rows, files)
	default:
		rows = append(rows, Component{Type: "row", Layout: "wide", ID: "storage-role", Level: LevelInfo, Dot: levelDot(LevelInfo),
			Subtitle: "Сёма в него не пишет",
			Caption:  "Зеркало одно: Сёма пишет только в «" + s.mirrorName() + "». Это хранилище осталось в справочнике — его можно проверить и убрать",
			Facts:    []Prop{{Label: "Куда смотрит", Value: i.Where}}})
	}
	add(Component{Type: "section", ID: "storage-facts", Title: "Что это", Children: rows})
}

// mirrorName — имя того зеркала, в которое Сёма пишет сейчас.
func (s *Server) mirrorName() string {
	if m := s.eng.MirrorStorage(); m != nil {
		return m.Name
	}
	return "—"
}

// storageParams — параметры полями строками настройки; сохраняет нижняя
// полоса действием `save_storage`. У местного параметров нет.
func (s *Server) storageParams(add func(Component), i storageInfo) {
	c := i.Conf
	var fields []Field
	switch c.Kind {
	case "s3":
		var conf storage.S3Config
		_ = json.Unmarshal([]byte(c.Config), &conf)
		fields = []Field{
			{Type: "text", Name: "endpoint", Label: "Адрес", Value: conf.Endpoint,
				Hint: "storage.yandexcloud.net · s3.eu-central-003.backblazeb2.com · 127.0.0.1:9000 для MinIO на этой машине"},
			{Type: "text", Name: "bucket", Label: "Bucket", Value: conf.Bucket},
			{Type: "text", Name: "region", Label: "Регион", Value: conf.Region, Hint: "ru-central1 у Яндекса; для MinIO можно пусто"},
			{Type: "text", Name: "prefix", Label: "Префикс", Value: conf.Prefix, Hint: "папка внутри bucket, можно пусто"},
			{Type: "toggle", Name: "ssl", Label: "HTTPS", Value: boolValue(conf.SSL)},
			s.secretFieldWithPick("access_secret", "Ключ доступа", conf.AccessSecret, "выбирается из «Ключей и секретов»: здесь только имя секрета"),
			s.secretFieldWithPick("secret_secret", "Секретный ключ", conf.SecretSecret, "выбирается из «Ключей и секретов»: здесь только имя секрета"),
		}
	case "yadisk", "webdav":
		var conf engine.YaDiskConf
		_ = json.Unmarshal([]byte(c.Config), &conf)
		fields = []Field{
			{Type: "text", Name: "folder", Label: "Папка на Диске", Value: conf.Folder,
				Hint: "куда складывать; копии лягут в подпапку «копии»"},
			s.secretFieldWithPick("token_secret", "Токен Диска", conf.Token,
				"имя секрета с OAuth-токеном из «Ключей и секретов», не сам токен. С ним копия едет родным API, и Сёма сверяет её размер и sha256 у Диска после выгрузки"),
		}
	default:
		return
	}
	add(Component{Type: "form", ID: "storage-params", Title: "Параметры", Style: formRows, Fields: fields,
		Submit: &Action{Action: "save_storage", Target: c.Name, Label: "Сохранить"}})
}

// storageKeys — ключи хранилища ссылкой на секрет: имя секрета и переход к нему.
// Значения ключей на экран не попадают: открывается экран секрета, а там
// значение закрыто так же.
func (s *Server) storageKeys(add func(Component), i storageInfo) {
	var names [][2]string // подпись, имя секрета
	switch i.Conf.Kind {
	case "s3":
		var conf storage.S3Config
		_ = json.Unmarshal([]byte(i.Conf.Config), &conf)
		names = [][2]string{{"Ключ доступа", conf.AccessSecret}, {"Секретный ключ", conf.SecretSecret}}
	case "yadisk", "webdav":
		var conf engine.YaDiskConf
		_ = json.Unmarshal([]byte(i.Conf.Config), &conf)
		names = [][2]string{{"Токен Диска", conf.Token}}
	}
	var rows []Component
	for n, pair := range names {
		label, name := pair[0], pair[1]
		row := Component{Type: "row", Layout: "wide", ID: fmt.Sprintf("storage-key-%d", n), Subtitle: label}
		switch {
		case name == "":
			row.Caption = "не выбран"
		case !s.vaultHas(name):
			row.Caption = "секрет «" + name + "» не найден в «Ключах и секретах»"
			withLevel(&row, LevelWarn)
		default:
			row.Caption = "секрет «" + name + "» · значение здесь не показывается"
			row.Actions = []Action{{Action: "$goto", Target: engine.SecretScreen(name, ""), Label: "Открыть секрет"}}
		}
		rows = append(rows, row)
	}
	add(Component{Type: "section", ID: "storage-keys", Title: "Ключи", Children: rows})
}

// saveStorage — «Сохранить» параметров хранилища. Сначала проверяется всё, потом
// пишется — половина формы не сохраняется. Непришедшее поле — «не трогали».
//
// Куда складывать (адрес, bucket, префикс у S3, папка у Диска) после того, как
// туда уже уехали файлы, не правится: в записях файлов остаётся пометка «в
// зеркале», и по новому адресу этих файлов нет. Нужно другое место — заводится
// другое хранилище. Регион, HTTPS и ключи менять можно: файлы от них не переезжают.
func (s *Server) saveStorage(req actionRequest) (string, error) {
	st := s.eng.Store()
	c, ok := s.storageByName(req.Target)
	if !ok {
		return "", fmt.Errorf("хранилища %q нет", req.Target)
	}
	text := func(name, old string) string {
		if v, has := req.Params[name]; has {
			return strings.TrimSpace(v)
		}
		return old
	}
	secretOf := func(name, old string) string {
		if v, has := req.Params[name]; has {
			return secretPick(v)
		}
		return old
	}
	var raw []byte
	switch c.Kind {
	case "s3":
		var conf storage.S3Config
		if err := json.Unmarshal([]byte(c.Config), &conf); err != nil {
			return "", err
		}
		was := conf
		conf.Endpoint = text("endpoint", conf.Endpoint)
		conf.Bucket = text("bucket", conf.Bucket)
		conf.Region = text("region", conf.Region)
		conf.Prefix = strings.Trim(text("prefix", conf.Prefix), "/")
		if v, has := req.Params["ssl"]; has {
			conf.SSL = truthy(v)
		}
		conf.AccessSecret = secretOf("access_secret", conf.AccessSecret)
		conf.SecretSecret = secretOf("secret_secret", conf.SecretSecret)
		switch {
		case conf.Endpoint == "" || conf.Bucket == "":
			return "", fmt.Errorf("нужны адрес и bucket")
		case conf.AccessSecret == "" || conf.SecretSecret == "":
			return "", fmt.Errorf("нужны оба ключа: выберите секреты «Ключ доступа» и «Секретный ключ»")
		}
		for _, name := range []string{conf.AccessSecret, conf.SecretSecret} {
			if err := s.secretNameExists(name); err != nil {
				return "", err
			}
		}
		if conf == was {
			return "", nil
		}
		if conf.Endpoint != was.Endpoint || conf.Bucket != was.Bucket || conf.Prefix != was.Prefix {
			if err := s.refuseMovedMirror(c.Name); err != nil {
				return "", err
			}
		}
		raw, _ = json.Marshal(conf)
	case "yadisk", "webdav":
		var conf engine.YaDiskConf
		if err := json.Unmarshal([]byte(c.Config), &conf); err != nil {
			return "", err
		}
		was := conf
		conf.Folder = strings.Trim(text("folder", conf.Folder), "/")
		conf.Token = secretOf("token_secret", conf.Token)
		if conf.Token == "" {
			return "", fmt.Errorf("нужен секрет с токеном Диска: без него Сёма не может спросить, что на Диске лежит")
		}
		if err := s.secretNameExists(conf.Token); err != nil {
			return "", err
		}
		if conf == was {
			return "", nil
		}
		if conf.Folder != was.Folder {
			if err := s.refuseMovedMirror(c.Name); err != nil {
				return "", err
			}
		}
		raw, _ = json.Marshal(conf)
	default:
		return "", fmt.Errorf("у хранилища вида «%s» нет параметров", c.Kind)
	}
	if err := st.SetStorageConfig(c.Name, string(raw)); err != nil {
		return "", err
	}
	// прежняя проверка относилась к прежним параметрам: новая — по кнопке
	st.NoteStorage(c.Name, "")
	s.mu.Lock()
	if strings.HasPrefix(s.lastStorageCheck, c.Name+": ") {
		s.lastStorageCheck = ""
	}
	s.mu.Unlock()
	return "", nil
}

// refuseMovedMirror — отказ менять место зеркала, в котором уже лежат файлы.
func (s *Server) refuseMovedMirror(name string) error {
	if done, _ := s.eng.Store().MirrorProgress(name); done > 0 {
		return fmt.Errorf("в «%s» уже лежит %s: они остались бы там, где были, а Сёма считал бы их лежащими по новому адресу. "+
			"Нужно другое место — заведите другое хранилище, а это уберите",
			name, plural(done, "файл", "файла", "файлов"))
	}
	return nil
}
