package api

// Действия «Ключей и секретов» и то, что общее для их экранов. Сами экраны —
// settings_secrets.go (раздел), settings_secret.go (один секрет),
// settings_secret_new.go (добавление). Значение секрета не попадает на экран
// никогда: только имя, вид, кто пользуется и кто брал в последний раз.

import (
	"fmt"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// secretKinds — пункты выбора вида, в порядке от лучшего к худшему.
var secretKinds = []string{
	"Ключ SSH — сгенерировать",
	"Ключ SSH — импортировать",
	"Токен",
	secretKindPassword,
	"Произвольный секрет",
}

const secretKindPassword = "Пароль"

// whenText — «сегодня 11:40», «вчера 18:02», «3 сентября 09:15».
func whenText(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	t = t.Local()
	now := time.Now()
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	switch day(t) {
	case day(now):
		return "сегодня " + t.Format("15:04")
	case day(now.AddDate(0, 0, -1)):
		return "вчера " + t.Format("15:04")
	}
	return fmt.Sprintf("%d %s %s", t.Day(), monthNames[t.Month()-1], t.Format("15:04"))
}

// secretsActions — действия экранов «Ключи и секреты». Каждое возвращает экран,
// на который вернуться: после «Добавить» — на экран нового секрета, после замены
// и выдачи — туда, где нажали.
func (s *Server) secretsActions(req actionRequest) (string, error) {
	switch req.Action {
	case "add_secret":
		name := strings.TrimSpace(req.Params["name"])
		value := req.Params["value"]
		// приватный ключ для импорта — своё поле, а значение токена и пароля —
		// поле secure: в него многострочный ключ не помещается. Прежний вызов с
		// ключом в «value» работает как работал
		private := value
		if v := req.Params["private"]; strings.TrimSpace(v) != "" {
			private = v
		}
		kind := req.Params["kind"]
		var err error
		switch kind {
		case secretKinds[0]:
			_, err = s.eng.GenerateSSHKey(name)
		case secretKinds[1]:
			_, err = s.eng.ImportSSHKey(name, private)
		case secretKinds[2]:
			err = s.eng.AddSecret(name, store.SecretToken, value)
		case secretKinds[3]:
			err = s.eng.AddSecret(name, store.SecretPassword, value)
		default:
			err = s.eng.AddSecret(name, store.SecretOther, value)
		}
		if err != nil {
			return "", err
		}
		// заметка человека вместо стандартной: «для чего он»
		if note := strings.TrimSpace(req.Params["note"]); note != "" {
			if err := s.eng.Store().SetSecretNote(name, note); err != nil {
				return "", err
			}
		}
		return entityAddr(secretsScreen, name, ""), nil
	case "dismiss_key":
		// прежняя карточка «ключ создан» ушла: публичная часть теперь всегда на
		// экране самого ключа. Действие осталось ради старых клиентов
		return secretsScreen, nil
	case "save_secret_value":
		return s.saveSecretValue(req)
	case "replace_secret":
		return stayOnSecrets(req, secretsScreen), s.eng.ReplaceSecret(req.Target, req.Params["value"])
	case "delete_secret":
		// после удаления экрана секрета больше нет: возвращаемся в список
		return secretsScreen, s.eng.DeleteSecret(req.Target)
	case "grant_secret", "revoke_secret":
		module, name := splitTarget(req.Target)
		return stayOnSecrets(req, secretsScreen), s.eng.GrantSecret(module, name, req.Action == "grant_secret")
	case "grant_pending_secret":
		// тумблер на экране установки: решение по одному имени
		s.mu.Lock()
		if s.pendingGrants == nil {
			s.pendingGrants = map[string]bool{}
		}
		s.pendingGrants[req.Target] = !s.pendingGrants[req.Target]
		s.mu.Unlock()
		return "modules", nil
	default:
		return "", errUnhandled
	}
}

// stayOnSecrets — после действия остаёмся там, где нажали: выдача в матрице
// не должна уводить с экрана секрета в список.
func stayOnSecrets(req actionRequest, def string) string {
	if strings.HasPrefix(req.Screen, secretsScreen) {
		return req.Screen
	}
	return def
}

// whoTook — кто просил секрет, без того, зачем просил. В журнале
// записано «ядро · srv-bots · systemctl status nginx», и вся команда
// уезжала в строку списка вместо имени: строка есть, смысла нет.
func whoTook(who string) string {
	parts := strings.Split(who, " · ")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, " · ")
}
