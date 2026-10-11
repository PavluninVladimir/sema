## 1. Песочница

- [x] 1.1 `sema.http.session()` (право `http`): объект с `get`, `post`, `request`; cookiejar на сессию, транспорт и перенаправления `scriptHTTP`; ответ `{ok, status, headers, body, url, error}` без Set-Cookie; тело-объект — формой или JSON; секреты в заголовках — как у `sema.http.*`.
- [x] 1.2 `sema.crypto.md5|sha1|sha256(text, {encoding})` без права; `utf-8` и `windows-1251`; пространство `crypto` известно проверке кода модуля.

## 2. Модель знает

- [x] 2.1 Подсказка о модулях и скилл «Модуль-интеграция»: вход с cookie и хешем пароля — через `sema.http.session()` и `sema.crypto`.

## 3. Проверка

- [x] 3.1 Тесты: вход с cookie на поддельном сайте (cookie уходит во второй запрос, Set-Cookie не виден, 500 — значение, модуль не выключен); md5 в windows-1251 совпадает с посчитанным в Go; проверка кода модуля пропускает `sema.crypto`.
- [x] 3.2 `go build`, `go vet`, `go test ./internal/...` зелёные; `CLAUDE.md` — строка в «Скрипты и модули».
