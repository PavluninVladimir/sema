package api

// Файлы через сервер и действия экрана «Хранилища»: отдача и загрузка байт,
// справочник хранилищ, наблюдаемые папки. Сами экраны — `settings_storages.go`
// (раздел), `settings_storage.go` (одно хранилище) и `settings_storage_new.go`
// («Добавить хранилище»).

import (
	"encoding/json"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/storage"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"path"
)

const filesUploadKey = "files.upload" // принимать ли загрузку через сервер

// handleFileGet отдаёт содержимое файла: из хранилища по ключу или по старому пути.
func (s *Server) handleFileGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	a, err := s.eng.Store().AttachmentByID(id)
	if err != nil {
		http.Error(w, "файл не найден", http.StatusNotFound)
		return
	}
	data, err := s.eng.FileBytes(a)
	if err != nil {
		http.Error(w, "файл недоступен: "+err.Error(), http.StatusNotFound)
		return
	}
	mime := a.MIME
	if mime == "" || mime == "application/octet-stream" {
		mime = engine.GuessMIME(a.Filename)
	}
	// файл из письма — чужой текст. HTML и SVG в адресе Сёмы исполнялись бы
	// с её cookie: такие отдаём на скачивание, а всё остальное — в
	// песочнице без сети и скриптов
	name := path.Base(strings.ReplaceAll(a.Filename, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = "файл"
	}
	disposition := "inline"
	if activeContent(mime) {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !strings.HasPrefix(strings.ToLower(mime), "application/pdf") {
		// PDF смотрит встроенный просмотрщик Chrome, а он — плагин, которого
		// песочница не пускает: PDF без неё, остальное в песочнице
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'")
	}
	w.Header().Set("Content-Disposition", disposition+`; filename*=UTF-8''`+urlEscape(name))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// handleFileUpload принимает файл (multipart, поле file): хэш, хранилище,
// источник «загрузка», модули узнают сразу.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if s.eng.Store().Setting(filesUploadKey, "true") == "false" {
		http.Error(w, "загрузка файлов выключена в настройках «Хранилища»", http.StatusForbidden)
		return
	}
	maxBytes := int64(s.eng.FilesMaxMB()) << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+1<<20)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		http.Error(w, fmt.Sprintf("файл больше %d МБ или неверный запрос: %v", s.eng.FilesMaxMB(), err), http.StatusBadRequest)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "нужно поле file", http.StatusBadRequest)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = hdr.Filename
	}
	att, created, err := s.eng.AddFile(r.Context(), name, hdr.Header.Get("Content-Type"), data,
		engine.FileSource{Kind: store.SourceUpload, Ref: strings.TrimSpace(r.FormValue("origin"))}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": att.ID, "created": created, "name": att.Filename, "kind": att.Kind})
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// пресеты адресов: гадать по названию нельзя, а вписывать руками
// «storage.yandexcloud.net» человеку каждый раз незачем
var s3Presets = [][2]string{
	{"свой адрес", ""},
	{"Yandex Object Storage", "storage.yandexcloud.net"},
	{"MinIO на этой машине", "127.0.0.1:9000"},
	{"Backblaze B2 (eu-central-003)", "s3.eu-central-003.backblazeb2.com"},
}

func presetNames() []string {
	out := make([]string, 0, len(s3Presets))
	for _, p := range s3Presets {
		out = append(out, p[0])
	}
	return out
}

func presetEndpoint(name string) string {
	for _, p := range s3Presets {
		if p[0] == name {
			return p[1]
		}
	}
	return ""
}

// whenChecked — хвост записи проверки: она заканчивается временем
// «… · 03.09 13:35», и в капсулу идёт только оно
func whenChecked(note string) string {
	if i := strings.LastIndex(note, " · "); i >= 0 {
		return strings.TrimSpace(note[i+len(" · "):])
	}
	return "только что"
}

func boolToggle(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f ГБ", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МБ", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n>>10)
	}
	return fmt.Sprintf("%d Б", n)
}

// noSecretPick — «не выбран» в списке секретов: у поля должно быть положение
// «ничего», иначе секрет назначается самим фактом открытия экрана
const noSecretPick = "—"

// secretPick — имя секрета из списка: «не выбран» значит пусто.
func secretPick(v string) string {
	v = strings.TrimSpace(v)
	if v == noSecretPick {
		return ""
	}
	return v
}

// storageNameOK — имя хранилища, которое можно положить в адрес экрана:
// «new» занято под «Добавить», а «~» отделяет вкладку.
func storageNameOK(name string) error {
	switch {
	case name == "" || name == "local":
		return fmt.Errorf("нужно своё название хранилища")
	case name == storNew:
		return fmt.Errorf("название «%s» занято: так зовётся экран добавления", storNew)
	case strings.Contains(name, "~"):
		return fmt.Errorf("в названии хранилища не должно быть «~»: так в адресе отделяется вкладка")
	}
	return nil
}

// storageByName — запись справочника по имени.
func (s *Server) storageByName(name string) (*store.StorageConf, bool) {
	all, err := s.eng.Store().Storages()
	if err != nil {
		return nil, false
	}
	for _, c := range all {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

// rememberStorageCheck — итог последней проверки: кто и что сказал. Хранится
// в памяти, а отказы S3 и Диска ещё и в самой записи хранилища (NoteStorage).
func (s *Server) rememberStorageCheck(name string, note string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastStorageCheck = name + ": ошибка: " + err.Error()
		return
	}
	s.lastStorageCheck = name + ": " + note
}

// checkStorage — «Проверить» хранилище. Шов для теста: настоящей проверке
// нужна сеть, а тесты в неё не ходят.
func (s *Server) checkStorage(name string) (string, error) {
	if s.checkStorageVia != nil {
		return s.checkStorageVia(name)
	}
	return s.eng.CheckStorage(name)
}

// filesActions — действия экрана «Хранилища».
func (s *Server) filesActions(req actionRequest) (string, error) {
	switch req.Action {
	case "set_yadisk_token":
		c, ok := s.storageByName(req.Target)
		if !ok || c.Kind != "yadisk" {
			return "", fmt.Errorf("хранилища «%s» нет или оно не Яндекс.Диск", req.Target)
		}
		var conf engine.YaDiskConf
		if err := json.Unmarshal([]byte(c.Config), &conf); err != nil {
			return "", err
		}
		имя := strings.TrimSpace(req.Params["value"])
		if err := s.secretNameExists(имя); err != nil {
			return "", err
		}
		conf.Token = имя
		raw, _ := json.Marshal(conf)
		return "", s.eng.Store().SetStorageConfig(req.Target, string(raw))
	case "add_yadisk":
		return s.addYaDisk(req)
	case "add_storage":
		return s.addS3(req)
	case "save_storage":
		return s.saveStorage(req)
	case "check_storage":
		note, err := s.checkStorage(req.Target)
		s.rememberStorageCheck(req.Target, note, err)
		return "", nil
	case "delete_storage":
		// с экрана хранилища назад — на список: самого хранилища больше нет
		return storagesScreen, s.eng.Store().DeleteStorage(req.Target)
	case "toggle_offload":
		return "", s.eng.SetOffload(!s.eng.OffloadOn())
	case "toggle_mirror":
		return "", s.eng.SetMirrorOn(!s.eng.MirrorOn())
	case "fix_mirror_task":
		return "", s.eng.SetMirrorOn(s.eng.MirrorOn())
	case "toggle_backup":
		return "", s.eng.SetBackupOn(!s.eng.BackupOn())
	case "fix_backup_task":
		// сводим по настройке: её человек и щёлкал, задача просто отстала
		return "", s.eng.SetBackupOn(s.eng.BackupOn())
	case "fix_offload_task":
		return "", s.eng.SetOffload(s.eng.OffloadOn())
	case "toggle_files_parse":
		return "", s.eng.SetFilesParse(!s.eng.FilesParseOn())
	case "toggle_files_upload":
		on := s.eng.Store().Setting(filesUploadKey, "true") != "false"
		return "", s.eng.Store().SetSetting(filesUploadKey, boolValue(!on))
	case "add_folder_source":
		path := strings.TrimSpace(req.Params["path"])
		if !strings.HasPrefix(path, "/") {
			return "", fmt.Errorf("нужен абсолютный путь внутри сервера, например /data/inbox")
		}
		return "", s.eng.Store().AddFileFolder(path, req.Params["delete_after"] == "true")
	case "toggle_folder_source", "toggle_folder_delete", "delete_folder_source", "save_folder":
		id, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		folders, err := s.eng.Store().FileFolders()
		if err != nil {
			return "", err
		}
		for _, f := range folders {
			if f.ID != id {
				continue
			}
			switch req.Action {
			case "toggle_folder_source":
				return "", s.eng.Store().SetFileFolder(id, "enabled", !f.Enabled)
			case "toggle_folder_delete":
				return "", s.eng.Store().SetFileFolder(id, "delete_after", !f.DeleteAfter)
			case "save_folder":
				// «удалять после приёма» приходит тумблером формы: непришедшее
				// поле — «не трогали», а снятый флажок форма присылает как false
				if v, has := req.Params["delete_after"]; has {
					return "", s.eng.Store().SetFileFolder(id, "delete_after", truthy(v))
				}
				return "", nil
			default:
				return "", s.eng.Store().DeleteFileFolder(id)
			}
		}
		return "", fmt.Errorf("папка не найдена")
	case "attach_file":
		chatID, err := strconv.ParseInt(req.Target, 10, 64)
		if err != nil {
			return "", err
		}
		fileID, err := strconv.ParseInt(strings.TrimSpace(req.Params["value"]), 10, 64)
		if err != nil {
			return "", fmt.Errorf("нужен номер файла")
		}
		return "", s.eng.AttachFileToChat(chatID, fileID)
	}
	return "", errUnhandled
}

// secretNameExists — секрет с таким именем есть в «Ключах и секретах». Пустое
// имя — «не назначен», и это не ошибка. Сюда пишется только имя: само значение
// секрета в справочник хранилищ не попадает.
func (s *Server) secretNameExists(name string) error {
	if name == "" {
		return nil
	}
	секреты, _ := s.eng.Store().VaultSecrets()
	for _, сек := range секреты {
		if сек.Name == name {
			return nil
		}
	}
	return fmt.Errorf("секрета «%s» нет в «Ключах и секретах» — заведите его там, сюда пишется только имя", name)
}

// addYaDisk — «Добавить» Яндекс.Диск: токен и папка. После добавления Сёма сам
// проверяет хранилище, а человека ведёт на его экран: итог проверки живёт там.
func (s *Server) addYaDisk(req actionRequest) (string, error) {
	name := strings.TrimSpace(req.Params["name"])
	if err := storageNameOK(name); err != nil {
		return "", err
	}
	conf := engine.YaDiskConf{
		Token:  secretPick(req.Params["token_secret"]),
		Folder: strings.Trim(strings.TrimSpace(req.Params["folder"]), "/"),
	}
	if conf.Token == "" {
		return "", fmt.Errorf("нужно имя секрета с токеном Диска: без него Сёма не может спросить, что на Диске лежит")
	}
	raw, _ := json.Marshal(conf)
	if err := s.eng.Store().AddStorage(name, "yadisk", string(raw)); err != nil {
		return "", err
	}
	note, err := s.checkStorage(name)
	s.rememberStorageCheck(name, note, err)
	return storageAddr(name), nil
}

// addS3 — «Добавить» S3-совместимое хранилище: адрес, bucket, ключи по именам
// секретов. Как и у Диска, дальше — проверка и экран хранилища.
func (s *Server) addS3(req actionRequest) (string, error) {
	name := strings.TrimSpace(req.Params["name"])
	if err := storageNameOK(name); err != nil {
		return "", err
	}
	// адрес из пресета — только когда своего не вписали: выбор из
	// списка не должен затирать то, что человек набрал руками
	endpoint := strings.TrimSpace(req.Params["endpoint"])
	if endpoint == "" {
		endpoint = presetEndpoint(strings.TrimSpace(req.Params["preset"]))
	}
	conf := storage.S3Config{
		Endpoint: endpoint, Bucket: strings.TrimSpace(req.Params["bucket"]),
		Region: strings.TrimSpace(req.Params["region"]), Prefix: strings.Trim(strings.TrimSpace(req.Params["prefix"]), "/"),
		SSL: req.Params["ssl"] != "false", AccessSecret: secretPick(req.Params["access_secret"]), SecretSecret: secretPick(req.Params["secret_secret"]),
	}
	if conf.Endpoint == "" || conf.Bucket == "" {
		return "", fmt.Errorf("нужны адрес и bucket")
	}
	if conf.AccessSecret == "" || conf.SecretSecret == "" {
		return "", fmt.Errorf("нужны оба ключа: выберите секреты «Ключ доступа» и «Секретный ключ» (заводятся в «Ключах и секретах»)")
	}
	raw, _ := json.Marshal(conf)
	if err := s.eng.Store().AddStorage(name, "s3", string(raw)); err != nil {
		return "", err
	}
	note, err := s.checkStorage(name)
	s.rememberStorageCheck(name, note, err)
	return storageAddr(name), nil
}

// activeContent — типы, которые браузер исполняет, а не показывает.
func activeContent(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	switch m {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml", "text/javascript", "application/javascript":
		return true
	}
	return false
}

// offloadCaption — что именно случится, если тумблер включить. Строка
// про удаление местных копий должна быть прочитана до нажатия, а не
// после.
func (s *Server) offloadCaption() string {
	дней := s.eng.OffloadDays()
	о := fmt.Sprintf("файл старше %s, уже лежащий в зеркале, перестаёт занимать место здесь и отдаётся из зеркала", plural(дней, "дня", "дней", "дней"))
	if м := s.eng.MirrorStorage(); м == nil {
		return о + " · зеркала нет — разгружать некуда"
	}
	if n, байт := s.eng.Store().OffloadedCount(s.eng.MirrorStorage().Name); n > 0 {
		return о + fmt.Sprintf(" · уже разгружено %s, %s", plural(n, "файл", "файла", "файлов"), humanBytes(байт))
	}
	return о + " · перед удалением местной копии Сёма забирает файл из зеркала и сверяет хэш"
}

// backupCaption — когда была последняя копия. Молчание здесь опаснее
// всего: копия, о которой никто не знает, что её нет, — не копия.
// mirrorCaption — не «включено», а сколько именно файлов уехало.
//
// Месяц зеркало стояло выключенным, и на экране это выглядело как
// порядок: строка «зеркало» была, а числа не было. Число — единственное,
// что отличает работающее зеркало от нарисованного.
func (s *Server) fileMirrorCaption() string {
	о := "копия каждого файла уезжает на Яндекс.Диск; читается файл всё равно с диска, а из зеркала — только если местная копия потерялась"
	уложено, total := s.eng.MirrorPass()
	if total == 0 {
		return о + " · файлов пока нет"
	}
	if !s.eng.MirrorOn() && уложено == 0 {
		return о + fmt.Sprintf(" · сейчас выключено, и ни один из %s не уехал: они только на этой машине",
			plural(total, "файла", "файлов", "файлов"))
	}
	return о + fmt.Sprintf(" · в зеркале %d из %d", уложено, total)
}

func (s *Server) backupCaption() string {
	о := "ночью Сёма снимает копию базы, проверяет её тем же самоосмотром, что и восстановление, и выгружает на Яндекс.Диск"
	когда := s.eng.Store().Setting("backup.last", "")
	if когда == "" {
		return о + " · копии ещё не было ни одной"
	}
	t, err := time.Parse(time.RFC3339, когда)
	if err != nil {
		return о
	}
	размер := ""
	if v := s.eng.Store().Setting("backup.last_size", ""); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			размер = ", " + humanBytes(n)
		}
	}
	return о + " · последняя " + whenText(t.Format(time.RFC3339)) + размер
}

// secretField — выбор из тех секретов, что уже заведены, а не строка с
// именем. Набранное руками имя, которого нет, молчит до «Проверить»: на
// экране всё выглядит заполненным, а хранилище не работает. Пустой
// список честно говорит, что заводить секреты надо в другом месте —
// значение секрета в эту форму не попадает и попасть не может.
func (s *Server) secretField(name, label, зачем string) Field {
	return s.secretFieldWithPick(name, label, "", зачем)
}

// secretFieldWithPick — то же поле, но помнит, что выбрано сейчас. Без
// этого экран каждый раз показывает первый секрет списка, и человек,
// сохранив форму, молча меняет пароль на чужой.
func (s *Server) secretFieldWithPick(name, label, текущее, зачем string) Field {
	секреты, _ := s.eng.Store().VaultSecrets()
	var names []string
	for _, сек := range секреты {
		names = append(names, сек.Name)
	}
	if len(names) == 0 {
		return Field{Type: "text", Name: name, Label: label, Value: текущее,
			Hint: "в «Ключах и секретах» пока пусто — заведите там пароль, и он появится здесь списком"}
	}
	// «—» первым: у поля должно быть положение «не выбран», иначе секрет
	// назначается самим фактом открытия экрана
	варианты := append([]string{noSecretPick}, names...)
	значение := noSecretPick
	for _, н := range names {
		if н == текущее {
			значение = н
		}
	}
	// секрет назначен, а в хранилище его нет (удалили): поле честно говорит
	// «не выбран», а не подставляет первый из списка
	return Field{Type: "picker", Name: name, Label: label, Options: варианты, Value: значение, Hint: зачем}
}

// mirrorCaption — строка про адрес хранилища. Второе значение — знаком
// ли Сёме этот вид: незнакомый рисуется тихой строкой без кнопок, потому
// что нажимать на нём нечего.
func (s *Server) mirrorCaption(c *store.StorageConf) (string, bool) {
	switch c.Kind {
	case "s3":
		var conf storage.S3Config
		_ = json.Unmarshal([]byte(c.Config), &conf)
		label := fmt.Sprintf("%s · bucket %s", conf.Endpoint, conf.Bucket)
		if conf.Prefix != "" {
			label += "/" + conf.Prefix
		}
		return label + fmt.Sprintf(" · ключи: секреты %s, %s", conf.AccessSecret, conf.SecretSecret), true
	case "yadisk", "webdav":
		var conf engine.YaDiskConf
		_ = json.Unmarshal([]byte(c.Config), &conf)
		label := "Яндекс.Диск"
		if conf.Folder != "" {
			label += " · папка «" + conf.Folder + "»"
		}
		if conf.Token != "" {
			label += " · токен: секрет " + conf.Token + " · сверка по sha256"
		} else {
			label += " · токена нет — не работает: выберите секрет с токеном на экране хранилища"
		}
		return label, true
	}
	return "тип «" + c.Kind + "» не поддерживается", false
}
