package engine

// Копия наружу родным API Яндекс.Диска.
//
// 11 сентября 2026 четыре копии подряд были объявлены неуехавшими, и все
// четыре в этот момент лежали на Диске целиком. Диск принимал 38 МБ до
// последнего байта, сохранял файл и закрывал соединение, не ответив;
// Сёма видела EOF, писала тревогу и снимала копию заново. Проверено
// хэшами: у копии, объявленной «не уехала», sha256 на Диске совпал с
// тем, что Сёма записала при снятии.
//
// Урок общий и он важнее самой почты: **успех выгрузки нельзя брать у
// соединения, если его можно спросить у хранилища**. Диск помнит размер
// и sha256 каждого файла. Поэтому здесь после заливки идёт сверка, и
// оборванный ответ перестаёт быть бедой: файл либо лежит и сходится,
// либо нет — и то и другое проверяемо.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/yadisk"
)

// diskBackupsAPI — клиент родного API, если у хранилища назван секрет с
// токеном. Нет токена — не беда: вернём nil, и копия поедет по-старому.
func (e *Engine) diskBackupsAPI() (*yadisk.Client, string, error) {
	if e.diskForTest != nil {
		return e.diskForTest()
	}
	m := e.MirrorStorage()
	if m == nil || (m.Kind != "yadisk" && m.Kind != "webdav") {
		return nil, "", nil
	}
	var conf YaDiskConf
	if err := json.Unmarshal([]byte(m.Config), &conf); err != nil {
		return nil, "", fmt.Errorf("настройки %s: %w", m.Name, err)
	}
	if strings.TrimSpace(conf.Token) == "" {
		return nil, "", nil
	}
	token, ok := e.coreSecret(conf.Token, "ядро · копия наружу")
	if !ok {
		return nil, "", fmt.Errorf("секрета %q с токеном Диска нет или он не открывается нынешним ключом", conf.Token)
	}
	folder := "копии"
	if conf.Folder != "" {
		folder = strings.Trim(conf.Folder, "/") + "/копии"
	}
	return yadisk.New(token), folder, nil
}

// takeBackup — выгрузка с проверкой у Диска.
//
// Возвращает строку-итог для журнала задачи. Ошибка возвращается только
// тогда, когда копии на Диске ПОСЛЕ всего действительно нет: обрыв
// ответа при целом файле ошибкой не считается — это ровно та беда, из-за
// которой всё и затевалось.
func (e *Engine) takeBackup(ctx context.Context, d *yadisk.Client, folder, path, name string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if err := d.CreateFolder(ctx, folder); err != nil {
		return "", err
	}
	if busy, total, err := d.Space(ctx); err == nil && total > 0 {
		if free := total - busy; free < st.Size() {
			return "", fmt.Errorf("на Диске свободно %s, а копии нужно %s",
				humanBytes(free), humanBytes(st.Size()))
		}
	}
	target := folder + "/" + name
	href, err := d.UploadLink(ctx, target)
	if err != nil {
		return "", err
	}
	log.Printf("копия уходит на %s: %s (%s)", yadisk.UploadNode(href), name, humanBytes(st.Size()))

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	pass := &uploadPass{total: st.Size(), start: time.Now(), step: 30 * time.Second}
	start := time.Now()
	troubleUpload := d.Put(ctx, href, io.TeeReader(f, pass), st.Size())
	if troubleUpload != nil {
		pass.result("оборвалась")
	} else {
		pass.result("дошла")
	}

	// Спрашиваем Диск, а не соединение. Даже когда заливка «упала»:
	// именно здесь и выясняется, что файл на месте.
	verify, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	z, troubleVerify := d.Stat(verify, target)
	if troubleVerify != nil {
		if troubleUpload != nil {
			return "", fmt.Errorf("%v; и на Диске её нет: %v", troubleUpload, troubleVerify)
		}
		return "", fmt.Errorf("залилась, но Диск о ней не рассказал: %w", troubleVerify)
	}
	if z.Size != st.Size() {
		return "", fmt.Errorf("на Диске %d байт вместо %d — копия неполна", z.Size, st.Size())
	}
	// sha256 Диск считает сам: сходится — значит доехал каждый байт
	if our := amountNearby(path); our != "" && z.Hash != "" && !strings.EqualFold(our, z.Hash) {
		return "", fmt.Errorf("на Диске лежит не то: sha256 %s вместо %s", shortHash(z.Hash), shortHash(our))
	}
	result := fmt.Sprintf("копия %s (%s) уехала за %s и сверена на Диске",
		name, humanBytes(st.Size()), time.Since(start).Round(time.Second))
	if troubleUpload != nil {
		// это не мелочь: соединение рвётся, и человек должен знать, что
		// «уехала» здесь — итог проверки, а не итог запроса
		result += fmt.Sprintf(" · ответ на заливку не пришёл (%s), но файл на месте и сходится", troubleUpload)
		log.Printf("копия %s: ответ оборвался, но Диск подтвердил размер и sha256", name)
	}
	// хэш рядом — чтобы копию можно было проверить и не спрашивая Диск
	if amount, err := os.ReadFile(path + ".sha256"); err == nil {
		if href2, err := d.UploadLink(ctx, target+".sha256"); err == nil {
			_ = d.Put(ctx, href2, strings.NewReader(string(amount)), int64(len(amount)))
		}
	}
	return result, nil
}

// removeOldAPI — держим последние N. Самую свежую не трогаем никогда,
// на коротком списке не делаем ничего: удаление здесь необратимо.
func (e *Engine) removeOldAPI(ctx context.Context, d *yadisk.Client, folder string) int {
	hold := e.BackupKeep()
	list, err := d.List(ctx, folder)
	if err != nil {
		return 0
	}
	var our []string
	for _, z := range list {
		if z.Kind != "dir" && strings.HasPrefix(z.Name, backupNameHead) && strings.HasSuffix(z.Name, ".tgz") {
			our = append(our, z.Name)
		}
	}
	if len(our) <= hold {
		return 0
	}
	sort.Strings(our) // имя несёт дату, значит порядок имён и есть порядок времени
	removed := 0
	for _, name := range our[:len(our)-hold] {
		if err := d.Delete(ctx, folder+"/"+name); err != nil {
			log.Printf("старую копию %s не убрал: %v", name, err)
			continue
		}
		_ = d.Delete(ctx, folder+"/"+name+".sha256")
		removed++
	}
	return removed
}

// amountNearby — хэш из файла «<копия>.sha256», который кладёт сам снимок.
func amountNearby(path string) string {
	b, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// HasDiskToken — видно ли на экране «Хранилища», что копии поедут
// родным API. Настройка, а не догадка по наличию секрета.
func (e *Engine) HasDiskToken() bool {
	d, _, err := e.diskBackupsAPI()
	return err == nil && d != nil
}
