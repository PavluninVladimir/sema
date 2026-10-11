package engine

// Роли моделей: кто разбирает письма, кто читает картинки, кто отвечает в
// чатах, кто считает векторы. У роли своя модель, своя запасная и свой
// таймаут — раньше таймауты были тремя числами, зашитыми в разные файлы, а
// отказ модели показывался человеку как ошибка.

import (
	"strconv"

	"context"
	"encoding/json"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"log"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
)

const (
	RoleFilter   = "Фильтр писем"
	RoleVision   = "Чтение картинок"
	RoleChat     = "Чаты"
	RoleEmbed    = "Эмбеддинги"
	RoleModules  = "Модули" // sema.llm в модулях: извлечение сроков, разбор писем
	rolesKey     = "roles.json"
	roleNoneName = "нет"
)

// Роль речи: модель выбирается по умению («слышит»), а не из всех подключённых
// подряд, — см. speech.go.
const RoleTranscribe = "Распознавание речи"

type Role struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	Spare   string `json:"spare"`    // «нет» — работать без запасной
	Timeout int    `json:"timeout"`  // секунды
	Last    string `json:"last"`     // чем получен последний результат
	LastErr string `json:"last_err"` // и когда основная модель подвела
}

// roleNames — роли справочника по именам, в порядке roleDefaults. Нужны, чтобы
// узнать, всё ли уже сохранено, не считая самих умолчаний; что список совпадает
// с roleDefaults, держит TestRoleNamesMatchDefaults.
var roleNames = []string{RoleFilter, RoleVision, RoleChat, RoleEmbed, RoleMemory, RoleModules,
	RoleSearch, RoleTranslate, RoleArbiter, RoleJudge, RoleTranscribe}

// roleDefaults — первичное заполнение справочника ролей. Читаем только
// сырые настройки: спрашивать VisionModel и EmbedModel нельзя — они сами
// смотрят в роли, и получится кольцо.
func (e *Engine) roleDefaults() []Role {
	active := e.ActiveProvider()
	return []Role{
		{Name: RoleFilter, Model: active, Spare: roleNoneName, Timeout: 30},
		{Name: RoleVision, Model: e.store.Setting(visionModelKey, active), Spare: roleNoneName, Timeout: 60},
		{Name: RoleChat, Model: active, Spare: roleNoneName, Timeout: 120},
		{Name: RoleEmbed, Model: e.store.Setting(embedModelKey, e.conf.Embeddings.Model),
			Spare: roleNoneName, Timeout: 60},
		// память по умолчанию на локальной модели: извлечение и сжатие не
		// должны стоить денег; «нет» — память только пишет и ищет
		{Name: RoleMemory, Model: e.localModelOrNone(), Spare: roleNoneName, Timeout: 300},
		// модули разбирают письма пачками — по умолчанию локально, если есть чем
		{Name: RoleModules, Model: e.localModelOrActive(), Spare: roleNoneName, Timeout: 120},
		// поиск требует модели, которая ходит в интернет: пока такой не
		// назначили, Сёма про поиск даже не заикается
		{Name: RoleSearch, Model: roleNoneName, Spare: roleNoneName, Timeout: 120},
		// перевод писем: по умолчанию локально, если есть чем. Перевод —
		// это текст письма целиком, и отдавать его наружу без нужды незачем
		{Name: RoleTranslate, Model: e.localModelOrActive(), Spare: roleNoneName, Timeout: 180},
		// суд над изменяющей командой: обе роли пустые, пока человек не
		// назначил им РАЗНЫЕ модели — одна модель, проверяющая себя, не проверка
		{Name: RoleArbiter, Model: roleNoneName, Spare: roleNoneName, Timeout: 120},
		{Name: RoleJudge, Model: roleNoneName, Spare: roleNoneName, Timeout: 120},
		// речь: своя модель, а нет её — gpt-4o-mini-transcribe, самая дешёвая
		// из облачных ($0.003 за минуту). Нет и её — «нет»: голос не слышен, и
		// в переписке об этом сказано честно
		{Name: RoleTranscribe, Model: e.defaultHearingModel(), Spare: roleNoneName, Timeout: 120},
	}
}

// localModelOrActive — локальная модель, а если её нет — активная.
func (e *Engine) localModelOrActive() string {
	if m := e.localModelOrNone(); m != roleNoneName {
		return m
	}
	return e.ActiveProvider()
}

func (e *Engine) Roles() []Role {
	raw := e.store.Setting(rolesKey, "")
	if raw == "" {
		return e.roleDefaults()
	}
	var out []Role
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out) == 0 {
		return e.roleDefaults()
	}
	// Умолчания считаются, только когда без них не обойтись: каждое из них
	// спрашивает модели, а список читают на каждом экране настроек, в каждой
	// строке сводки и в каждой проверке двери. При ста семидесяти моделях это
	// сотни чтений из базы ради ответа «всё уже сохранено».
	have := make(map[string]bool, len(out))
	for _, r := range out {
		have[r.Name] = true
	}
	complete := true
	for _, name := range roleNames {
		complete = complete && have[name]
	}
	if complete {
		return out
	}
	// роль, появившаяся после сохранения списка, дописывается из умолчаний
	for _, d := range e.roleDefaults() {
		found := false
		for _, r := range out {
			if r.Name == d.Name {
				found = true
			}
		}
		if !found {
			out = append(out, d)
		}
	}
	return out
}

// localModelOrNone — первая локальная модель из подключённых или «нет».
func (e *Engine) localModelOrNone() string {
	for _, c := range e.userModels() {
		if !e.ModelIsCloud(c.Name) {
			return c.Name
		}
	}
	return roleNoneName
}

func (e *Engine) SaveRoles(list []Role) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return e.store.SetSetting(rolesKey, string(raw))
}

// Role возвращает роль по имени (или значения по умолчанию).
func (e *Engine) Role(name string) Role {
	for _, r := range e.Roles() {
		if r.Name == name {
			return r
		}
	}
	for _, r := range e.roleDefaults() {
		if r.Name == name {
			return r
		}
	}
	return Role{Name: name, Timeout: 60, Spare: roleNoneName}
}

// SetRoleField меняет одно поле роли — так работают списки в строке.
func (e *Engine) SetRoleField(name, field, value string) error {
	list := e.Roles()
	for i := range list {
		if list[i].Name != name {
			continue
		}
		switch field {
		case "model", "spare":
			// роль речи берёт только модель с нужным умением: остальные ей
			// отвечали бы ошибкой на первом же голосовом
			if err := e.checkRoleAbility(name, value); err != nil {
				return err
			}
			if field == "model" {
				list[i].Model = value
			} else {
				list[i].Spare = value
			}
		case "timeout":
			var v int
			if _, err := fmt.Sscanf(value, "%d", &v); err == nil && v > 0 {
				list[i].Timeout = v
			}
		}
		return e.SaveRoles(list)
	}
	return nil
}

// noteRole запоминает, чем получен последний результат: молчаливый переход
// на запасную модель не должен выглядеть как «всё работает».
func (e *Engine) noteRole(name, by string) {
	list := e.Roles()
	for i := range list {
		if list[i].Name == name {
			list[i].Last = by + " · " + time.Now().Format("02.01 15:04")
			_ = e.SaveRoles(list)
			return
		}
	}
}

// AskRole выполняет запрос моделью роли: при отказе или таймауте работу
// делает запасная, и в ответе видно, кем он получен.
func (e *Engine) AskRole(ctx context.Context, roleName, prompt string) (string, string, error) {
	return e.AskRoleSpare(ctx, roleName, prompt, true)
}

// AskRoleSpare — тот же запрос, но с решением, звать ли запасную модель.
// Фоновая работа зовёт облачную запасную только когда локальная не
// справляется: обычно она просто ждёт своей очереди.
func (e *Engine) AskRoleSpare(ctx context.Context, roleName, prompt string, allowSpare bool) (string, string, error) {
	if err := e.budgetCheck(ctx); err != nil {
		return "", "", err
	}
	r := e.Role(roleName)
	// модель подряд не отвечает — даём ей передышку, а не ждём таймаут
	// каждый раз: иначе задача висит и «Здоровье» краснеет на пустом месте
	if until, ok := e.roleCooldown(roleName); ok {
		// на передышке помогает запасная, если её разрешили
		if allowSpare && r.Spare != "" && r.Spare != roleNoneName {
			if p := e.provider(r.Spare); p != nil {
				cctx, cancel := context.WithTimeout(ctx, time.Duration(max(r.Timeout, 1))*time.Second)
				defer cancel()
				out, serr := p.Complete(cctx, prompt)
				if serr == nil {
					e.noteRole(roleName, r.Spare+" (запасная)")
					return out, r.Spare, nil
				}
				return "", r.Spare, humanModelErr(serr)
			}
		}
		return "", r.Model, fmt.Errorf("модель %q роли %q не отвечала подряд — передышка до %s, поменяйте модель на «Настройки → Модели»",
			r.Model, roleName, until.Format("15:04"))
	}
	timeout := time.Duration(r.Timeout) * time.Second
	if timeout <= 0 {
		timeout = time.Minute
	}
	try := func(model string) (string, error) {
		p := e.provider(model)
		if p == nil {
			return "", fmt.Errorf("модель %q не подключена", model)
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return p.Complete(cctx, prompt)
	}
	out, err := try(r.Model)
	if err == nil {
		e.noteRole(roleName, r.Model)
		e.clearRoleFails(roleName)
		return out, r.Model, nil
	}
	log.Printf("роль %q: модель %s не справилась: %v", roleName, r.Model, err)
	e.noteRoleFail(roleName, r.Model, err)
	if ctx.Err() != nil {
		// время работы вышло целиком: запасную звать некуда, её отказ был бы
		// мгновенным и обманчивым
		return "", r.Model, fmt.Errorf("работа прервана по времени: %w", ctx.Err())
	}
	if !allowSpare || r.Spare == "" || r.Spare == roleNoneName {
		return "", r.Model, humanModelErr(err)
	}
	out, serr := try(r.Spare)
	if serr != nil {
		return "", r.Spare, humanModelErr(serr)
	}
	e.noteRole(roleName, r.Spare+" (запасная)")
	return out, r.Spare, nil
}

// RoleProvider — модель роли как обычный провайдер, для мест, где нужен он.
func (e *Engine) RoleProvider(roleName string) llm.Provider {
	return e.provider(e.Role(roleName).Model)
}

const roleFailsBeforeRest = 3

// roleCooldown — идёт ли передышка у роли и до какого времени.
func (e *Engine) roleCooldown(roleName string) (time.Time, bool) {
	raw := e.store.Setting("role.cooldown."+roleName, "")
	if raw == "" {
		return time.Time{}, false
	}
	until, err := time.Parse(time.RFC3339, raw)
	if err != nil || time.Now().After(until) {
		e.store.DeleteSettingKey("role.cooldown." + roleName)
		return time.Time{}, false
	}
	return until, true
}

// RoleRestMinutes — сколько длится передышка (настройка roles.rest_min).
func (e *Engine) RoleRestMinutes() int {
	v, _ := strconv.Atoi(e.store.Setting("roles.rest_min", "30"))
	if v <= 0 {
		v = 30
	}
	return v
}

// ClearRoleErr — человек закрыл замечание «модель не тянет роль»: отметка
// снимается, пока модель не подведёт снова. Замечание без способа его
// закрыть висело неделями с подписью «модель сменили» — и его переставали
// читать вместе с теми, что важны.
func (e *Engine) ClearRoleErr(roleName string) error {
	list := e.Roles()
	for i := range list {
		if list[i].Name == roleName {
			list[i].LastErr = ""
			return e.SaveRoles(list)
		}
	}
	return fmt.Errorf("роли «%s» нет", roleName)
}

func (e *Engine) clearRoleFails(roleName string) {
	e.store.DeleteSettingKey("role.fails." + roleName)
	e.store.DeleteSettingKey("role.cooldown." + roleName)
	e.CloseNotification("role:"+roleName, store.NotifyDone, "модель снова отвечает")
	// и саму отметку о сбое: её никто не снимал, и «Стоит посмотреть»
	// показывало вчерашний отказ модели, которая с тех пор весь день
	// отвечает. Замечание, которое не гаснет, перестают читать целиком —
	// вместе с теми, что важны
	list := e.Roles()
	for i := range list {
		if list[i].Name == roleName && list[i].LastErr != "" {
			list[i].LastErr = ""
			_ = e.SaveRoles(list)
			return
		}
	}
}

// modelsScreenName — экран «Модели и роли»: адрес нужен уведомлениям.
const modelsScreenName = "settings:models"

// modelDenial — модель не молчит, а отвечает отказом: шлюз вернул
// finish_reason=refusal или сама модель написала «не могу выполнить».
// Такое ожиданием не лечится — нужна другая модель.
func modelDenial(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, p := range []string{"refusal", "refused the request", "i can't help", "i cannot help",
		"не могу помочь", "не могу выполнить", "content_policy", "policy violation"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// RoleSpare — запасная модель роли, как её назначил человек.
func (e *Engine) RoleSpare(roleName string) string {
	for _, r := range e.Roles() {
		if r.Name == roleName {
			return r.Spare
		}
	}
	return ""
}

// MakeSpareMain — «Сменить модель» из уведомления: запасная
// становится основной, а прежняя основная уходит в запасные. Решение
// человека, но одним нажатием: искать роль среди полусотни моделей после
// отказа — работа, которой быть не должно.
func (e *Engine) MakeSpareMain(roleName string) error {
	list := e.Roles()
	for i := range list {
		if list[i].Name != roleName {
			continue
		}
		rec := list[i].Spare
		if rec == "" || rec == "нет" {
			return fmt.Errorf("у роли «%s» нет запасной модели", roleName)
		}
		list[i].Spare, list[i].Model = list[i].Model, rec
		list[i].LastErr = ""
		if err := e.SaveRoles(list); err != nil {
			return err
		}
		e.clearRoleFails(roleName)
		return nil
	}
	return fmt.Errorf("роли «%s» нет", roleName)
}

// countRoleFail — отказы подряд; после третьего роль уходит на передышку,
// а человек получает уведомление.
func (e *Engine) countRoleFail(roleName, model string, err error) {
	// нет связи — модель ни при чём: передышка и карточка «поменяйте
	// модель» только сбили бы с толку, когда починить надо Wi-Fi
	if llm.IsNetworkErr(err) {
		return
	}
	key := "role.fails." + roleName
	n, _ := strconv.Atoi(e.store.Setting(key, "0"))
	n++
	_ = e.store.SetSetting(key, strconv.Itoa(n))
	if n < roleFailsBeforeRest {
		return
	}
	until := time.Now().Add(time.Duration(e.RoleRestMinutes()) * time.Minute)
	_ = e.store.SetSetting("role.cooldown."+roleName, until.Format(time.RFC3339))
	// «не отвечает» и «отказалась отвечать» — разные беды, и чинятся
	// по-разному: молчание проходит само, отказ — нет, ждать его
	// бессмысленно (14 сентября 2026, claude-fable-5-1 у роли «Арбитр»)
	denial := modelDenial(err)
	title := "Роль «" + roleName + "»: модель " + model + " не отвечает"
	whatDo := "поменять модель или запасную на «Настройки → Модели», либо дождаться, когда модель заработает"
	result := "отказов подряд " + strconv.Itoa(n) + " · передышка до " + until.Format("15:04") + ", работа роли пока не идёт"
	if moneyRanOut(err) {
		title = "Роль «" + roleName + "»: у провайдера модели " + model + " закончились деньги"
		whatDo = "пополнить счёт провайдера или назначить роли модель другого провайдера на «Настройки → Модели»; ждать бессмысленно — сама модель не заработает"
		result = "отказов подряд " + strconv.Itoa(n) + " · провайдер отказывает: на счёте нет денег"
	}
	if denial {
		title = "Роль «" + roleName + "»: модель " + model + " отказывается выполнять запрос"
		whatDo = "ждать бессмысленно: модель не молчит, а отвечает отказом. Назначьте роли другую модель — «Сменить модель» рядом"
		result = "отказов подряд " + strconv.Itoa(n) + " · это отказ модели, а не сбой связи"
	}
	actions := []store.NotifyAction{
		{Label: "Открыть модели", Kind: "goto", Screen: modelsScreenName, Style: "primary"},
		{Label: "Понял", Kind: "done"},
	}
	if denial {
		// запасная у роли уже назначена человеком — поставить её основной
		// можно отсюда, не разбираясь в списке из полусотни моделей
		if rec := e.RoleSpare(roleName); rec != "" && rec != "нет" && rec != model {
			actions = append([]store.NotifyAction{{Label: "Поставить запасную «" + rec + "»",
				Kind: "core", Op: "role_use_spare", Ref: roleName, Style: "primary"}}, actions...)
			actions[1].Style = ""
		}
	}
	_, _ = e.Notify(&store.Notification{
		Key: "role:" + roleName, Source: "core", Level: "warn",
		Title:   title,
		Summary: result,
		Fields: []store.NotifyField{{Label: "Модель", Value: model}, {Label: "Ошибка", Value: humanModelErr(err).Error(), Level: "warn"},
			{Label: "Что делать", Value: whatDo}},
		Actions: actions,
	})
}

// noteRoleFail запоминает отказ основной модели: молчаливый переход на
// запасную не должен выглядеть как «всё работает».
func (e *Engine) noteRoleFail(name, model string, err error) {
	list := e.Roles()
	for i := range list {
		if list[i].Name == name {
			list[i].LastErr = fmt.Sprintf("%s · %s · %s", model,
				time.Now().Format("02.01 15:04"), humanModelErr(err))
			_ = e.SaveRoles(list)
			e.countRoleFail(name, model, err)
			return
		}
	}
}
