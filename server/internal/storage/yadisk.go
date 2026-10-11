package storage

// Яндекс.Диск как хранилище файлов — родным API, а не WebDAV.
//
// WebDAV у Яндекса снят 11 сентября 2026, и не из любви к новизне. У
// шлюза нельзя спросить, что у него лежит: он принимал файл целиком,
// сохранял его и закрывал соединение без ответа. Для копий это стоило
// четырёх «потерянных» архивов, которые всё это время лежали на Диске
// целыми. Для зеркала было бы то же самое, только тише — зеркало не
// поднимает тревог.
//
// Родное API отвечает кодами, а про каждый файл помнит размер и sha256.
// Значит «положил» можно проверить, а не принять на веру.
//
// Раскладка прежняя: ключ `ab/cd/<sha>` ложится в один уровень
// `<папка>/ab/<sha>`. Обратимость раскладки важнее её красоты — `Get`
// обязан найти то, что положил `Put`, иначе зеркало тихо превращается
// в свалку.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/yadisk"
)

// YaDisk — хранилище на Яндекс.Диске.
type YaDisk struct {
	name   string
	folder string
	client *yadisk.Client
	// каталоги первого уровня, для которых папка уже заведена. Их 256 на
	// всю жизнь зеркала, и кэш только растёт
	known map[string]bool
}

// NewYaDisk — хранилище поверх токена.
func NewYaDisk(name, token, folder string) *YaDisk {
	return &YaDisk{
		name:   name,
		folder: strings.Trim(strings.TrimSpace(folder), "/"),
		client: yadisk.New(token),
		known:  map[string]bool{},
	}
}

func (d *YaDisk) Name() string { return d.name }
func (d *YaDisk) Kind() string { return "yadisk" }

// Path — местного пути нет: файл лежит на Диске, читать через Get.
func (d *YaDisk) Path(string) string { return "" }

func (d *YaDisk) path(key string) string {
	parts := strings.Split(strings.Trim(key, "/"), "/")
	name := parts[len(parts)-1]
	level := "прочее"
	if len(name) >= 2 {
		level = name[:2]
	} else if len(parts) > 1 {
		level = parts[0]
	}
	if d.folder == "" {
		return level + "/" + name
	}
	return d.folder + "/" + level + "/" + name
}

func (d *YaDisk) keyLevel(key string) string {
	p := d.path(key)
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	return p[:i]
}

// Put кладёт содержимое под ключ. Повторный Put того же ключа — не
// ошибка: содержательный ключ значит, что содержимое то же самое.
func (d *YaDisk) Put(key string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), fileTimeout(int64(len(data))))
	defer cancel()
	if lvl := d.keyLevel(key); lvl != "" && !d.known[lvl] {
		if err := d.client.CreateFolder(ctx, lvl); err != nil {
			return err
		}
		d.known[lvl] = true
	}
	path := d.path(key)
	href, err := d.client.UploadLink(ctx, path)
	if err != nil {
		return err
	}
	uploadErr := d.client.Put(ctx, href, bytes.NewReader(data), int64(len(data)))
	// спрашиваем Диск, а не соединение: оборванный ответ при целом файле
	// не потеря — этому нас научили копии
	rec, err := d.client.Stat(ctx, path)
	if err != nil {
		if uploadErr != nil {
			return uploadErr
		}
		return err
	}
	if rec.Size != int64(len(data)) {
		return fmt.Errorf("на Диске %d байт вместо %d", rec.Size, len(data))
	}
	return nil
}

func (d *YaDisk) Get(key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	r, err := d.client.Fetch(ctx, d.path(key))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Delete — только по прямой просьбе: на Диске удаление идёт мимо
// Корзины и необратимо, поэтому в горячем пути его нет.
func (d *YaDisk) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	return d.client.Delete(ctx, d.path(key))
}

// Stat — сколько занято на Диске целиком. Число файлов Сёма знает из
// своей базы: обход дерева стоил бы тысячи запросов ради одного числа.
func (d *YaDisk) Stat() (int, int64) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	used, _, err := d.client.Space(ctx)
	if err != nil {
		return 0, 0
	}
	return 0, used
}

// Check — «Проверить» на экране «Хранилища».
func (d *YaDisk) Check() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	if d.folder != "" {
		if err := d.client.CreateFolder(ctx, d.folder); err != nil {
			return "", err
		}
	}
	used, total, err := d.client.Space(ctx)
	if err != nil {
		return "", err
	}
	result := "токен подошёл · запись и чтение работают"
	if total > 0 {
		result = fmt.Sprintf("токен подошёл · занято %s из %s, свободно %s · запись и чтение работают",
			humanBytes(used), humanBytes(total), humanBytes(total-used))
	}
	return result, nil
}

// Client — тому, кому нужен не «файл под ключом», а поток: копия целиком
// не помещается в память Raspberry Pi.
func (d *YaDisk) Client() *yadisk.Client { return d.client }

// Folder — куда этот хранитель кладёт.
func (d *YaDisk) Folder() string { return d.folder }

const readTimeout = 2 * time.Minute

// fileTimeout — сколько ждать заливку. Считаем по худшему: минута на
// мегабайт плюс минута сверху, но не меньше двух минут.
func fileTimeout(bytes int64) time.Duration {
	dur := time.Duration(bytes/(1<<20)+1) * time.Minute
	if dur < 2*time.Minute {
		return 2 * time.Minute
	}
	return dur
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f ГБ", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f МБ", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f КБ", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d Б", b)
}
