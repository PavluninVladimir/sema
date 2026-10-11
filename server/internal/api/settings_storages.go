package api

// Раздел «Хранилища»: где лежат файлы и откуда они берутся. Две вкладки —
// «Где лежат» и «Откуда берутся»; у каждого хранилища свой экран
// (settings_storage.go), а «Добавить хранилище» — свой экран
// (settings_storage_new.go).
//
// До 2 октября 2026 это была одна лента: у хранилища в строке стояли адрес,
// bucket, имена двух секретов, прогресс зеркала, итог проверки хвостом и
// четыре кнопки, а под списком — две формы добавления (девять полей одна,
// три другая), развёрнутые всегда. Теперь:
//
//   - «Где лежат» — тонкие строки: имя, не больше двух капсул (как себя
//     чувствует и какую роль играет), одна фраза, одна кнопка «Проверить», «···»,
//     клик по всей строке ведёт на экран хранилища; итог проверки и её ошибка —
//     на самом экране, а в строке только капсула;
//   - «Откуда берутся» — источники файлов тумблерами и наблюдаемые папки
//     тонкими строками, у каждой «Настройки» раскрываются на месте: «удалять
//     после приёма» — полем формы, а не отдельной кнопкой.
//
// Адреса:
//
//	settings:storages                   вкладка «Где лежат»
//	settings:storages~sources           вкладка «Откуда берутся»
//	settings:storages@new               «Добавить хранилище» (S3-совместимое)
//	settings:storages@new~yadisk        «Добавить хранилище» (Яндекс.Диск)
//	settings:storages@<имя>             экран хранилища
//
// Поведение не менялось: файлы, зеркало, разгрузка, копия наружу и приём из
// папок работают как работали, а действия остались под прежними именами.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/storage"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const storagesScreen = "settings:storages"

// Внутренние вкладки «Хранилищ». Где лежат файлы и откуда они берутся —
// два разных вопроса: первый решают раз в год, заводя зеркало, второй
// трогают каждый раз, когда появляется новая папка со сканами.
const (
	storTabPlaces  = ""        // Где лежат: хранилища, зеркала, переезд
	storTabSources = "sources" // Откуда берутся: источники и наблюдаемые папки

	// storNew — «хранилище» с этим именем означает экран добавления
	storNew = "new"
	// storNewDisk — вкладка экрана добавления: Яндекс.Диск (первая — S3)
	storNewDisk = "yadisk"
)

// storagesAddr — разобранный адрес экрана «Хранилищ».
type storagesAddr struct {
	Name string // имя хранилища; пусто — раздел; «new» — «Добавить хранилище»
	Tab  string // вкладка раздела или вид на экране добавления
}

func parseStoragesAddr(screen string) storagesAddr {
	_, id, tab := splitEntity(screen)
	return storagesAddr{Name: strings.TrimSpace(id), Tab: tab}
}

// storageAddr — адрес экрана хранилища.
func storageAddr(name string) string { return entityAddr(storagesScreen, name, "") }

func (s *Server) buildStorages(doc *Document, screen string) error {
	a := parseStoragesAddr(screen)
	doc.Layout = "wide"
	switch {
	case a.Name == storNew:
		return s.buildStorageNew(doc, a.Tab)
	case a.Name != "":
		return s.buildStorage(doc, a)
	}
	return s.buildStoragesSection(doc, a.Tab)
}

// storageInfo — что известно о хранилище: для строки списка и для его экрана.
// Считается один раз на хранилище, а не отдельно каждым из двух.
type storageInfo struct {
	Conf  *store.StorageConf
	Known bool // вид знаком Сёме
	Local bool // местное, основное
	// Mirror — это то зеркало, в которое Сёма пишет: первое не местное
	// (Engine.MirrorStorage). Spare — не местное, но Сёма в него не пишет
	Mirror, Spare bool
	Note          string // последняя проверка из записи хранилища
	Done, All     int    // сколько файлов уже в зеркале из скольких
	Files         int    // сколько файлов лежит в самом хранилище (по его счёту)
	Bytes         int64
	StatOK        bool
	// Where — куда смотрит хранилище: адрес и bucket, папка на Диске, папка
	// сервера. Problem — что мешает работать: не названы секреты, нет токена
	Where, Problem string
}

// storageInfos — все хранилища с их сведениями. Список секретов читается один
// раз, а не на каждое хранилище.
func (s *Server) storageInfos() []storageInfo {
	st := s.eng.Store()
	all, _ := st.Storages()
	mirror := s.eng.MirrorStorage()
	have := map[string]bool{}
	secrets, _ := st.VaultSecrets()
	for _, sec := range secrets {
		have[sec.Name] = true
	}
	out := make([]storageInfo, 0, len(all))
	for _, c := range all {
		out = append(out, s.storageInfoOf(c, mirror, have))
	}
	return out
}

func (s *Server) storageInfoOf(c *store.StorageConf, mirror *store.StorageConf, have map[string]bool) storageInfo {
	st := s.eng.Store()
	i := storageInfo{Conf: c, Note: st.StorageNote(c.Name)}
	switch c.Kind {
	case "local":
		i.Known, i.Local = true, true
		i.Where = "папка сервера " + s.eng.FilesDir()
		// считается по диску, и только у местного: у внешнего хранилища счёт — это
		// поход в сеть (список объектов S3 до минуты) и запись в журнал секрета при
		// каждом открытии экрана; сколько файлов уехало, знает база (`MirrorProgress`)
		i.Files, i.Bytes, i.StatOK = s.eng.StorageStat(c.Name)
	case "s3":
		i.Known = true
		var conf storage.S3Config
		_ = json.Unmarshal([]byte(c.Config), &conf)
		i.Where = conf.Endpoint + " · bucket " + conf.Bucket
		if conf.Prefix != "" {
			i.Where += "/" + conf.Prefix
		}
		switch {
		case conf.AccessSecret == "" || conf.SecretSecret == "":
			i.Problem = "не выбраны секреты с ключами"
		case !have[conf.AccessSecret]:
			i.Problem = "секрета «" + conf.AccessSecret + "» нет в «Ключах и секретах»"
		case !have[conf.SecretSecret]:
			i.Problem = "секрета «" + conf.SecretSecret + "» нет в «Ключах и секретах»"
		}
	case "yadisk", "webdav":
		i.Known = true
		var conf engine.YaDiskConf
		_ = json.Unmarshal([]byte(c.Config), &conf)
		i.Where = "Яндекс.Диск"
		if conf.Folder != "" {
			i.Where += " · папка «" + conf.Folder + "»"
		}
		switch {
		case conf.Token == "":
			i.Problem = "токена нет — не работает"
		case !have[conf.Token]:
			i.Problem = "секрета «" + conf.Token + "» нет в «Ключах и секретах»"
		}
	}
	if !i.Local {
		i.Mirror = mirror != nil && mirror.Name == c.Name
		i.Spare = i.Known && !i.Mirror
		if i.Known {
			i.Done, i.All = st.MirrorProgress(c.Name)
		}
	}
	return i
}

// failed — последняя проверка кончилась ошибкой.
func (i storageInfo) failed() bool { return strings.HasPrefix(i.Note, "ошибка") }

// lagging — зеркало отстаёт: часть файлов туда не уехала.
func (i storageInfo) lagging() int {
	if !i.Mirror {
		return 0
	}
	return i.All - i.Done
}

// pill — как хранилище себя чувствует, одним словом.
func (i storageInfo) pill() Badge {
	switch {
	case !i.Known:
		return Badge{Text: "не поддерживается", Level: LevelWarn}
	case i.Local:
		return Badge{Text: "основное"}
	case i.Problem != "":
		return Badge{Text: "не настроено", Level: LevelWarn}
	case i.failed():
		return Badge{Text: "не отвечает", Level: LevelAlert}
	case i.Note == "":
		return Badge{Text: "не проверялось", Level: LevelInfo}
	}
	return Badge{Text: "проверено " + whenChecked(i.Note), Level: LevelOK}
}

// role — какую роль хранилище играет: зеркало, в которое Сёма пишет, или
// «не используется». Пусто у местного: его роль называет капсула «основное».
func (i storageInfo) role() string {
	switch {
	case i.Mirror:
		return "зеркало"
	case i.Spare:
		return "не используется"
	}
	return ""
}

// level — цвет полосы строки и шапки: беда видна, не читая подписи.
func (i storageInfo) level() string {
	switch {
	case i.failed():
		return LevelAlert
	case !i.Known, i.Problem != "", i.lagging() > 0:
		return LevelWarn
	}
	return ""
}

// phrase — одна фраза строки списка.
func (i storageInfo) phrase() string {
	switch {
	case !i.Known:
		return "тип «" + i.Conf.Kind + "» не поддерживается"
	case i.Local:
		out := "папка сервера"
		if i.StatOK {
			out += " · " + plural(i.Files, "файл", "файла", "файлов") + " · " + humanBytes(i.Bytes)
		}
		return out
	}
	out := i.Where
	switch {
	case i.Mirror:
		out += fmt.Sprintf(" · в зеркале %d из %d", i.Done, i.All)
	case i.Spare:
		out += " · Сёма в него не пишет"
	}
	if i.Problem != "" {
		out += " · " + i.Problem
	}
	return out
}

// storageTile — буква и цвет плитки: местное тёмное, у каждого вида свой.
func storageTile(c *store.StorageConf) (letter, color string) {
	letter = firstRuneOfName(c.Name)
	switch c.Kind {
	case "local":
		return letter, "#17181C"
	case "s3":
		return letter, "#E0821E"
	case "yadisk", "webdav":
		return letter, "#2E7DD1"
	}
	return letter, "#8E8E93"
}

// storageRow — тонкая строка списка: имя, не больше двух капсул, одна фраза,
// одна кнопка, «···». Вся строка ведёт на экран хранилища; итог проверки,
// адрес и ключи — на самом экране.
func (s *Server) storageRow(i storageInfo) Component {
	c := i.Conf
	letter, color := storageTile(c)
	row := Component{Type: "row", Layout: "wide", ID: "storage-" + c.Name, Subtitle: c.Name,
		Avatar: letter, AvatarColor: color, AvatarShape: "square", Caption: i.phrase()}
	// у местного одна капсула: «основное» и есть его состояние и роль
	row.Badges = []Badge{i.pill()}
	if role := i.role(); role != "" {
		row.Badges = append(row.Badges, Badge{Text: role})
	}
	if lvl := i.level(); lvl != "" {
		withLevel(&row, lvl)
	}
	row.Actions = []Action{{Action: "$goto", Target: storageAddr(c.Name), Label: c.Name}}
	if i.Known {
		row.Actions = append(row.Actions, Action{Action: "check_storage", Target: c.Name, Label: "Проверить"})
	}
	if !i.Local && i.Known {
		row.Menu = []Action{removeStorageAction(c.Name)}
	}
	return row
}

// removeStorageAction — «Убрать хранилище…» с вопросом: файлы там остаются, а
// Сёма перестаёт туда писать.
func removeStorageAction(name string) Action {
	return Action{Action: "delete_storage", Target: name, Label: "Убрать…", Style: "destructive",
		Confirm:   "Убрать хранилище «" + name + "»? Файлы там останутся, Сёма перестанет туда писать.",
		ConfirmOK: "Убрать"}
}

func (s *Server) buildStoragesSection(doc *Document, tab string) error {
	doc.Heading = "Хранилища"
	st := s.eng.Store()
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	total, bytes, bySource := st.FileStats()
	doc.Subheading = fmt.Sprintf("%s на %s. Байты отдаёт только сервер: клиенту и модулям всё равно, где файл лежит.",
		plural(total, "файл", "файла", "файлов"), humanBytes(bytes))

	infos := s.storageInfos()
	folders, _ := st.FileFolders()
	tabs := []innerTab{
		{ID: storTabPlaces, Name: "Где лежат", Count: strconv.Itoa(len(infos))},
		{ID: storTabSources, Name: "Откуда берутся", Count: strconv.Itoa(len(folders))},
	}
	// зеркало заведено, а файлы в него не уехали — это видно с полосы
	// вкладок: «0 из 260» человек иначе находит, только заглянув
	for _, i := range infos {
		if i.Local {
			continue
		}
		if i.Mirror && i.All-i.Done > 0 {
			tabs[0].Count, tabs[0].Level = fmt.Sprintf("%d не в зеркале", i.All-i.Done), LevelWarn
		}
	}
	setSubtabs(doc, storagesScreen, tab, tabs)
	// «Добавить хранилище» — единственная кнопка шапки раздела и стоит на обеих
	// вкладках: шапка не меняется от вкладки к вкладке
	doc.ToolbarActions = []Action{{Action: "$goto", Target: entityAddr(storagesScreen, storNew, ""),
		Label: "Добавить хранилище", Style: "primary"}}

	if tab == storTabSources {
		return s.fileSources(add, st, bySource, folders)
	}

	rows := make([]Component, 0, len(infos))
	for _, i := range infos {
		rows = append(rows, s.storageRow(i))
	}
	add(Component{Type: "section", ID: "storages-list", Title: "Где лежат файлы · " + strconv.Itoa(len(infos)), Children: rows})

	left, all := s.eng.FilesMigrateProgress()
	mig := Component{Type: "row", Layout: "wide", ID: "files-migrate", Subtitle: "Переезд старых вложений",
		Caption: "считает хэш, записывает источник «письмо» и ящик, переносит файл под ключ по хэшу",
		Actions: []Action{{Action: "$goto", Target: "tasks", Label: "В задачах"}}}
	if left == 0 {
		mig.Badges = []Badge{{Text: fmt.Sprintf("готово · %d", all), Level: LevelOK}}
	} else {
		mig.Badges = []Badge{{Text: fmt.Sprintf("идёт · %d из %d", all-left, all), Level: LevelWarn}}
	}
	add(Component{Type: "section", ID: "storages-migrate", Title: "Переезд", Children: []Component{mig}})

	// ссылки на файлы для чужих систем (file-share-links)
	add(Component{Type: "form", ID: "storages-share", Title: "Ссылки на файлы", Tier: tierRare, Style: formRows, Fields: []Field{
		s.settingField("share.days", "share_days", "Срок ссылки на файл",
			"Сколько дней ссылка, выданная Сёмой или вами, открывает файл без входа; отозвать можно раньше", "дн."),
	}, Submit: &Action{Action: "save_share_days", Label: "Сохранить"}})

	add(Component{Type: "section", ID: "storages-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "Локальное хранилище всегда основное, внешнее — зеркало: файл читается с диска, а при потере копии берётся из зеркала. " +
			"Яндекс.Диск Сёма держит родным API: после каждой записи он спрашивает у Диска размер и sha256, потому что оборванный ответ неотличим от потери файла. " +
			"Удаление на Диске идёт мимо Корзины — безвозвратно. Ключи хранилищ — в «Ключах и секретах»: здесь называются только их имена."}}})
	return nil
}

// fileSources — откуда к Сёме приходят файлы: письма, загрузка, папки на
// диске, модули. Тумблеры источников и справочник папок.
func (s *Server) fileSources(add func(Component), st *store.Store, bySource map[string]int, folders []*store.FileFolder) error {
	// расхождение настройки и самой задачи. Это уже случилось на живой
	// Сёме: человек включил «Копию наружу», настройка стала «1», задача
	// осталась выключенной — и копия не снималась, а экран показывал
	// «включено». Теперь тумблер щёлкает обе, но старое расхождение надо
	// показать и дать чем починить
	if !s.eng.BackupOn() && s.eng.BackupTaskOn() || s.eng.BackupOn() && !s.eng.BackupTaskOn() {
		add(Component{Type: "alarm", Subtitle: "«Копия наружу» включена, а задача — нет",
			Caption: "копия не снимается: у дела оказалось два выключателя, и они разошлись. Нажмите — щёлкнет оба",
			Actions: []Action{{Action: "fix_backup_task", Label: "Свести", Style: "primary"}}})
	}
	if s.eng.OffloadOn() != s.eng.OffloadTaskOn() {
		add(Component{Type: "alarm", Subtitle: "«Разгрузка файлов» включена, а задача — нет",
			Caption: "файлы не разгружаются: настройка и задача разошлись. Нажмите — щёлкнет оба",
			Actions: []Action{{Action: "fix_offload_task", Label: "Свести", Style: "primary"}}})
	}

	upload := st.Setting(filesUploadKey, "true") != "false"
	src := []Component{
		{Type: "row", Layout: "wide", ID: "src-mail", Subtitle: "Вложения писем",
			Caption: fmt.Sprintf("все ящики · файлы до %d МБ · сохраняются и из спама · %s", s.eng.FilesMaxMB(), plural(bySource[store.SourceMail], "файл", "файла", "файлов")),
			Badges:  []Badge{{Text: "всегда"}}},
		{Type: "row", Layout: "wide", ID: "src-upload", Subtitle: "Загрузка через сервер",
			Caption: fmt.Sprintf("перетащили в окно Сёмы или прислали в POST /api/files · источник «загрузка» · %s", plural(bySource[store.SourceUpload], "файл", "файла", "файлов")),
			Toggle:  boolToggle(upload),
			Actions: []Action{{Action: "toggle_files_upload", Label: "toggle"}}},
		{Type: "row", Layout: "wide", ID: "src-offload", Subtitle: "Разгружать старые файлы",
			Caption: s.offloadCaption(),
			Toggle:  boolToggle(s.eng.OffloadOn()),
			Actions: []Action{{Action: "toggle_offload", Label: "toggle"}}},
		{Type: "row", Layout: "wide", ID: "src-mirror", Subtitle: "Зеркало файлов",
			Caption: s.fileMirrorCaption(),
			Toggle:  boolToggle(s.eng.MirrorOn()),
			Actions: []Action{{Action: "toggle_mirror", Label: "toggle"}}},
		{Type: "row", Layout: "wide", ID: "src-backup", Subtitle: "Копия наружу по ночам",
			Caption: s.backupCaption(),
			Toggle:  boolToggle(s.eng.BackupOn()),
			Actions: []Action{{Action: "toggle_backup", Label: "toggle"}}},
		{Type: "row", Layout: "wide", ID: "src-parse", Subtitle: "Разбирать, что внутри",
			Caption: "У пришедшего файла Сёма спрашивает модель, чек это или документ, кто получатель и на какую сумму, и пишет это прямо в запись файла · очередь «Разбор файлов»",
			Toggle:  boolToggle(s.eng.FilesParseOn()),
			Actions: []Action{{Action: "toggle_files_parse", Label: "toggle"}}},
		{Type: "row", Layout: "wide", ID: "src-folder", Subtitle: "Папки на диске",
			Caption: fmt.Sprintf("раз в минуту Сёма забирает новые файлы из наблюдаемых папок · источник «папка» · %s · справочник ниже", plural(bySource[store.SourceFolder], "файл", "файла", "файлов")),
			Badges:  []Badge{{Text: "по справочнику", Color: "#7952B3"}}},
		{Type: "row", Layout: "wide", ID: "src-module", Subtitle: "Модули",
			Caption: fmt.Sprintf("по праву «files» через sema.files.add · источник «модуль» · %s", plural(bySource[store.SourceModule], "файл", "файла", "файлов")),
			Badges:  []Badge{{Text: "по правам", Color: "#8E8E93"}}},
	}
	add(Component{Type: "section", ID: "src-list", Title: "Источники файлов", Children: src})

	// справочник папок: тонкие строки, у каждой «Настройки» раскрываются на месте
	frows := make([]Component, 0, len(folders))
	for _, f := range folders {
		frows = append(frows, s.folderRow(f))
	}
	if len(frows) == 0 {
		frows = append(frows, emptyState("Папок нет",
			"Сёма забирает новые файлы из папок, которые вы назвали: сканы, выгрузки, всё, что кладёт сканер. Добавьте путь ниже", "folder"))
	}
	add(Component{Type: "section", ID: "folders-list", Title: "Наблюдаемые папки · " + strconv.Itoa(len(folders)), Children: frows})
	add(Component{Type: "form", ID: "add-folder", Title: "Новая папка", Collapsed: true, Style: formRows, Fields: []Field{
		{Type: "text", Name: "path", Label: "Путь внутри сервера", Hint: "например, /data/inbox"},
		{Type: "toggle", Name: "delete_after", Label: "Удалять файлы после приёма", Value: "false",
			Hint: folderDeleteHint},
	}, Submit: &Action{Action: "add_folder_source", Label: "Добавить", Style: "primary"}})
	add(Component{Type: "section", ID: "folders-how", Title: "Как это устроено", Collapsed: true, Children: []Component{{Type: "text", Style: "secondary",
		Text: "В Docker папка мака видна только через том: docker run … -v ~/Scans:/data/inbox. Файл, который ещё пишется, Сёма берёт на следующей минуте; уже принятые помнит по пути, размеру и времени."}}})
	return nil
}

// folderDeleteHint — что значит «удалять после приёма»: необратимо для файла в
// папке, поэтому подсказка говорит это словами и до включения, а не после.
const folderDeleteHint = "Необратимо для файла в папке: после приёма Сёма стирает его оттуда, остаётся только копия у Сёмы. " +
	"Стирает лишь тот файл, который принят без ошибки"

// folderRow — наблюдаемая папка: путь, состояние, сколько принято; тумблер
// «включена» в строке, «Настройки» — раскрытием на месте, «Убрать…» — в «···».
func (s *Server) folderRow(f *store.FileFolder) Component {
	id := strconv.FormatInt(f.ID, 10)
	caption := "раз в минуту"
	if f.Taken > 0 {
		caption += " · " + plural(f.Taken, "файл принят", "файла принято", "файлов принято")
	}
	if f.DeleteAfter {
		caption += " · удалять после приёма"
	} else {
		caption += " · файлы остаются в папке"
	}
	row := Component{Type: "row", Layout: "wide", ID: "folder-" + id, Subtitle: f.Path,
		Toggle:  boolToggle(f.Enabled),
		Actions: []Action{{Action: "toggle_folder_source", Target: id, Label: "toggle"}},
		Menu: []Action{{Action: "delete_folder_source", Target: id, Label: "Убрать…", Style: "destructive",
			Confirm: "Убрать папку «" + f.Path + "» из наблюдаемых? Принятые файлы останутся у Сёмы.", ConfirmOK: "Убрать"}},
		Expandable: true, Expand: "Настройки"}
	switch {
	case !f.Enabled:
		row.Badges = []Badge{{Text: "выключена"}}
	case f.LastErr != "":
		row.Badges = []Badge{{Text: "ошибка", Level: LevelAlert}}
		caption += " · " + f.LastErr
		withLevel(&row, LevelAlert)
	default:
		row.Badges = []Badge{{Text: "читается", Level: LevelOK}}
	}
	row.Caption = caption
	row.Children = []Component{{Type: "form", ID: "folder-form-" + id, Style: formRows,
		Fields: []Field{{Type: "toggle", Name: "delete_after", Label: "Удалять файлы после приёма",
			Hint: folderDeleteHint, Value: boolValue(f.DeleteAfter)}},
		Submit: &Action{Action: "save_folder", Target: id, Label: "Сохранить"}}}
	return row
}
