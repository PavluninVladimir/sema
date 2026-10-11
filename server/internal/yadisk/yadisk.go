// Пакет yadisk — родное API Яндекс.Диска.
//
// WebDAV у Яндекса — шлюз совместимости, и он подвёл там, где подводить
// нельзя: 11 сентября 2026 четыре копии подряд были приняты Диском
// целиком и сохранены, а ответа на запрос не пришло ни на один. Сёма
// видела обрыв соединения, считала выгрузку неудачной, поднимала
// тревогу и снимала копию заново — при том, что копия уже лежала на
// Диске. Проверено хэшами: у файла, объявленного «не уехал», sha256 на
// Диске совпал с тем, что Сёма записала при снятии.
//
// Отсюда устройство этого пакета. Тело идёт на выделенный узел загрузки
// по одноразовой ссылке, а не на общий шлюз. И, что важнее, у Диска
// можно СПРОСИТЬ, что у него лежит: он хранит размер и sha256 каждого
// файла и отдаёт их в ответе. Значит «дошло» перестаёт быть словом
// соединения и становится проверяемым фактом.
package yadisk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiAddr = "https://cloud-api.yandex.net/v1/disk"

// Client — токен и ничего больше. Значение токена наружу не отдаётся
// никогда: ни в ошибках, ни в журнале.
type Client struct {
	token string
	addr  string // куда ходим; в работе это адресAPI, в тестах — поддельный Диск
	http  *http.Client
}

func New(token string) *Client {
	return &Client{token: strings.TrimSpace(token), addr: apiAddr, http: &http.Client{}}
}

// NewForTests — клиент, который ходит на поддельный Диск. Нужен
// проверкам движка: настоящий Диск в тесте не поднять, а правило
// «успех спрашиваем у хранилища» без него не проверить.
func NewForTests(addr, token string) *Client {
	return &Client{token: token, addr: strings.TrimSuffix(addr, "/") + "/v1/disk", http: &http.Client{}}
}

// Entry — файл или папка на Диске.
type Entry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Hash     string `json:"sha256"`
	Kind     string `json:"type"` // file | dir
	Modified string `json:"modified"`
}

func (c *Client) request(ctx context.Context, method, where string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, where, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "OAuth "+c.token)
	return req, nil
}

// readBody — разбор с оглядкой на то, что в теле ошибки бывает что угодно:
// режем длину, иначе чужой HTML уедет в журнал целиком.
func (c *Client) readBody(req *http.Request, what string) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		var out struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(body, &out) == nil && out.Message != "" {
			return nil, fmt.Errorf("%s: %s — %s", what, resp.Status, out.Message)
		}
		return nil, fmt.Errorf("%s: %s", what, resp.Status)
	}
	return body, nil
}

func diskPath(path string) string { return "disk:/" + strings.Trim(path, "/") }

// CreateFolder — папка. Уже существует (409) — это не ошибка.
func (c *Client) CreateFolder(ctx context.Context, folder string) error {
	// Диск не заводит промежуточные уровни сам: «Сёма/копии» без «Сёма»
	// кончается отказом 409 на несуществующего родителя
	parts := strings.Split(strings.Trim(folder, "/"), "/")
	for i := range parts {
		q := url.Values{"path": {diskPath(strings.Join(parts[:i+1], "/"))}}
		req, err := c.request(ctx, "PUT", c.addr+"/resources?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusConflict {
			return fmt.Errorf("не завести папку %q: %s", parts[i], resp.Status)
		}
	}
	return nil
}

// UploadLink — шаг первый: куда лить тело.
func (c *Client) UploadLink(ctx context.Context, path string) (string, error) {
	q := url.Values{"path": {diskPath(path)}, "overwrite": {"true"}}
	req, err := c.request(ctx, "GET", c.addr+"/resources/upload?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	body, err := c.readBody(req, "Диск не дал ссылку на загрузку")
	if err != nil {
		return "", err
	}
	var out struct {
		Href string `json:"href"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("ответ Диска не разобрался: %w", err)
	}
	if out.Href == "" {
		return "", fmt.Errorf("Диск вернул пустую ссылку на загрузку")
	}
	return out.Href, nil
}

// UploadNode — имя узла из ссылки: его полезно назвать в журнале.
// Тело идёт не туда же, куда остальные запросы, и когда с сетью что-то
// не так, первый вопрос — «а куда, собственно, лили».
func UploadNode(href string) string {
	if u, err := url.Parse(href); err == nil {
		return u.Hostname()
	}
	return "неизвестный узел"
}

// Put — тело на узел загрузки. Ссылка подписана, второй раз
// удостоверяться не нужно.
func (c *Client) Put(ctx context.Context, href string, r io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", href, r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("узел загрузки отказал: %s — %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// Stat — что Диск знает о файле: размер и sha256. Это и есть
// проверка доставки: соединение могло оборваться, а файл лежать.
func (c *Client) Stat(ctx context.Context, path string) (Entry, error) {
	q := url.Values{"path": {diskPath(path)}, "fields": {"name,size,sha256,type,modified"}}
	req, err := c.request(ctx, "GET", c.addr+"/resources?"+q.Encode(), nil)
	if err != nil {
		return Entry{}, err
	}
	body, err := c.readBody(req, "Диск не рассказал о файле")
	if err != nil {
		return Entry{}, err
	}
	var rec Entry
	if err := json.Unmarshal(body, &rec); err != nil {
		return Entry{}, err
	}
	return rec, nil
}

// List — что лежит в папке, свежее сверху.
func (c *Client) List(ctx context.Context, folder string) ([]Entry, error) {
	q := url.Values{"path": {diskPath(folder)}, "limit": {"500"},
		"fields": {"_embedded.items.name,_embedded.items.size,_embedded.items.sha256,_embedded.items.type,_embedded.items.modified"}}
	req, err := c.request(ctx, "GET", c.addr+"/resources?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	body, err := c.readBody(req, "Диск не отдал список")
	if err != nil {
		return nil, err
	}
	var out struct {
		Embedded struct {
			Items []Entry `json:"items"`
		} `json:"_embedded"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Embedded.Items, nil
}

// Delete — насовсем, мимо Корзины: копии лежат не для того, чтобы
// занимать место ещё и в ней.
func (c *Client) Delete(ctx context.Context, path string) error {
	q := url.Values{"path": {diskPath(path)}, "permanently": {"true"}}
	req, err := c.request(ctx, "DELETE", c.addr+"/resources?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	_, err = c.readBody(req, "не убрать с Диска "+path)
	return err
}

// Space — сколько занято и сколько всего.
func (c *Client) Space(ctx context.Context) (used, total int64, err error) {
	req, err := c.request(ctx, "GET", c.addr+"?fields=used_space,total_space", nil)
	if err != nil {
		return 0, 0, err
	}
	body, err := c.readBody(req, "Диск не сказал про место")
	if err != nil {
		return 0, 0, err
	}
	var out struct {
		Used  int64 `json:"used_space"`
		Total int64 `json:"total_space"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, err
	}
	return out.Used, out.Total, nil
}

// Check — токен живой и Диск отвечает.
func (c *Client) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, _, err := c.Space(ctx)
	return err
}

// Fetch — содержимое файла с Диска. Как и заливка, идёт в два шага:
// сначала одноразовая ссылка, потом сам файл.
func (c *Client) Fetch(ctx context.Context, path string) (io.ReadCloser, error) {
	q := url.Values{"path": {diskPath(path)}}
	req, err := c.request(ctx, "GET", c.addr+"/resources/download?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	body, err := c.readBody(req, "Диск не дал ссылку на скачивание")
	if err != nil {
		return nil, err
	}
	var out struct {
		Href string `json:"href"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.Href == "" {
		return nil, fmt.Errorf("Диск вернул пустую ссылку на скачивание")
	}
	req2, err := http.NewRequestWithContext(ctx, "GET", out.Href, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req2)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("файл не отдался: %s", resp.Status)
	}
	return resp.Body, nil
}
