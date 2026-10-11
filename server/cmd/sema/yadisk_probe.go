package main

// Выгрузка на Диск родным API вместо WebDAV.
//
// WebDAV у Яндекса — шлюз совместимости: тело файла идёт на тот же
// webdav.yandex.ru, что и всё остальное. Родное API устроено в два
// шага: сначала спрашиваем, куда класть, и получаем одноразовую
// подписанную ссылку на узел загрузки (uploader*.disk.yandex.net),
// потом льём тело прямо туда.
//
// Для нас это важно не только протоколом. Узел загрузки — другой
// адрес и другая сеть, а значит на роутере он может пойти другим
// маршрутом, чем webdav.yandex.ru. Поэтому проба печатает, куда
// именно она пошла и с какой скоростью: если маршрут другой и полки
// нет — копию надо возить этим путём.
//
// Токен читается из хранилища секретов по имени и наружу не печатается.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const yaDiskAPI = "https://cloud-api.yandex.net/v1/disk"

// diskToken — OAuth-токен из хранилища секретов. Значение не печатаем
// нигде и ни при какой ошибке.
func diskToken(conf *config.Config, name string) (string, error) {
	st, err := store.OpenReadOnly(conf.DB)
	if err != nil {
		return "", fmt.Errorf("база: %w", err)
	}
	defer st.Close()
	box, err := secret.New(os.Getenv(conf.KeyEnvName()))
	if err != nil || box == nil {
		return "", fmt.Errorf("нет ключа шифрования в %s: токен не прочитать", conf.KeyEnvName())
	}
	st.UseSecretBox(box)
	v, ok := st.SecretValue(name)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("секрета %q нет или он не открывается нынешним ключом — заведите его на «Ключах и секретах»", name)
	}
	fmt.Printf("токен %q прочитан (%d знаков, значение не печатается)\n", name, len(v))
	return strings.TrimSpace(v), nil
}

// uploadURL — шаг первый: спрашиваем у Диска одноразовую ссылку.
func uploadURL(ctx context.Context, token, path string) (string, error) {
	q := url.Values{"path": {path}, "overwrite": {"true"}}
	req, err := http.NewRequestWithContext(ctx, "GET", yaDiskAPI+"/resources/upload?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode/100 != 2 {
		// в ответе Диска токена нет, но на всякий случай режем длину
		return "", fmt.Errorf("Диск не дал ссылку: %s — %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var answer struct {
		Href   string `json:"href"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", fmt.Errorf("ответ Диска не разобрался: %w", err)
	}
	if answer.Href == "" {
		return "", fmt.Errorf("Диск вернул пустую ссылку")
	}
	return answer.Href, nil
}

// whereItWent — на какой узел и на какой адрес. Ради этого проба и
// затевалась: маршрут у узла загрузки может быть не тот, что у шлюза.
func whereItWent(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return "адрес не разобрался"
	}
	fromHost := u.Hostname()
	addrs, err := net.LookupHost(fromHost)
	if err != nil {
		return fromHost + " (адрес не разрешился: " + err.Error() + ")"
	}
	return fromHost + " → " + strings.Join(addrs, ", ")
}

// yaDiskPut — шаг второй: тело прямо на узел загрузки.
func yaDiskPut(conf *config.Config, filePath, folder, secretName string) error {
	token, err := diskToken(conf, secretName)
	if err != nil {
		return err
	}
	st, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	name := filepath.Base(filePath)
	target := "disk:/" + strings.Trim(folder, "/") + "/" + name

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	href, err := uploadURL(ctx, token, target)
	cancel()
	if err != nil {
		return err
	}
	fmt.Printf("класть сюда: %s\n", whereItWent(href))
	fmt.Printf("файл %s (%s) → %s\n", name, humanBytes(st.Size()), target)

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	progress := &probeProgress{total: st.Size(), started: time.Now(), step: 20 * time.Second}
	// срок большой нарочно: проба должна упереться в Яндекс или в канал,
	// а не в нас
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel2()
	req, err := http.NewRequestWithContext(ctx2, "PUT", href, io.TeeReader(f, progress))
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		return fmt.Errorf("за %s ушло %s: %w", elapsed.Round(time.Second), humanBytes(progress.sent), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("узел загрузки отказал: %s — %s", resp.Status, strings.TrimSpace(string(body)))
	}
	fmt.Printf("ДОШЛО: %s за %s (%s)\n", humanBytes(st.Size()), elapsed.Round(time.Second), progress.speed())
	return nil
}

type probeProgress struct {
	total, sent int64
	started     time.Time
	lastReport  time.Time
	step        time.Duration
}

func (pr *probeProgress) Write(p []byte) (int, error) {
	pr.sent += int64(len(p))
	if time.Since(pr.lastReport) >= pr.step {
		pr.lastReport = time.Now()
		fmt.Printf("  %s из %s, %s\n", humanBytes(pr.sent), humanBytes(pr.total), pr.speed())
	}
	return len(p), nil
}

func (pr *probeProgress) speed() string {
	secs := time.Since(pr.started).Seconds()
	if secs <= 0 {
		return "скорость неизвестна"
	}
	return fmt.Sprintf("%.0f КБ/с", float64(pr.sent)/secs/1024)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d МБ", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n>>10)
	}
	return fmt.Sprintf("%d Б", n)
}

// yaDiskList — что лежит в папке на Диске. Ответ «201 Created» от узла
// загрузки — это его слово о себе; правда о Диске спрашивается у Диска.
func yaDiskList(conf *config.Config, folder, secretName string) error {
	token, err := diskToken(conf, secretName)
	if err != nil {
		return err
	}
	q := url.Values{"path": {"disk:/" + strings.Trim(folder, "/")}, "limit": {"100"},
		"sort": {"-modified"}, "fields": {"_embedded.items.name,_embedded.items.size,_embedded.items.modified,_embedded.items.sha256"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", yaDiskAPI+"/resources?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Диск не отдал список: %s — %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var answer struct {
		Embedded struct {
			Items []struct {
				Name     string `json:"name"`
				Size     int64  `json:"size"`
				Modified string `json:"modified"`
				Hash     string `json:"sha256"`
			} `json:"items"`
		} `json:"_embedded"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return err
	}
	fmt.Printf("на Диске в %q — %d:\n", folder, len(answer.Embedded.Items))
	for _, f := range answer.Embedded.Items {
		fmt.Printf("  %-32s %12d Б  %s  sha256 %s\n", f.Name, f.Size, f.Modified, f.Hash)
	}
	return nil
}

// yaDiskRemove — удалить файл с Диска. Нужно и для уборки старых копий:
// задача держит их BackupKeep штук, а сегодня не убрала ни одной — она
// считала неудачей каждый свой заход.
func yaDiskRemove(conf *config.Config, folder, name, secretName string) error {
	token, err := diskToken(conf, secretName)
	if err != nil {
		return err
	}
	q := url.Values{"path": {"disk:/" + strings.Trim(folder, "/") + "/" + name}, "permanently": {"true"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", yaDiskAPI+"/resources?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "OAuth "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("не убрал %q: %s — %s", name, resp.Status, strings.TrimSpace(string(body)))
	}
	fmt.Printf("убрано с Диска: %s\n", name)
	return nil
}
