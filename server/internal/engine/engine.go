// Package engine — мозг Сёмы: опрашивает почту, применяет выученные правила,
// зовёт LLM для новых писем, исполняет решения пользователя и чистит Junk.
package engine

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // пояса в бинарнике: в образе нет /usr/share/zoneinfo

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/embed"
	"github.com/PavluninVladimir/sema/server/internal/google"
	"github.com/PavluninVladimir/sema/server/internal/llm"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/mcp"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
	"github.com/PavluninVladimir/sema/server/internal/yadisk"
)

type Engine struct {
	// счётчикиДляТеста — подмена списка пар «сделано из всего».
	//
	// Настоящие пары приходят от живых воркеров, и поднять их в проверке
	// нечем; проверять же надо саму мерку — стоит счётчик или идёт.
	// Единственное поле движка, существующее ради тестов, и потому названо
	// прямо, а не спрятано за безобидным именем.
	countersForTest func() []workCounter

	// дискДляТеста — подмена хранилища копий в проверке «две записи спорят».
	// Настоящее хранилище — Яндекс.Диск с токеном из секретов; поднять его в
	// тесте нечем, а проверять надо само сравнение: свой итог против чужого
	// ответа. Второе и последнее поле движка, существующее ради тестов.
	diskForTest func() (*yadisk.Client, string, error)

	// карточки «Здоровья» с коротким сроком: точка в меню на каждом экране
	healthMu         sync.Mutex
	healthCardsCache []HealthCard
	healthLevel      string
	healthWhen       time.Time
	sentMu           sync.Mutex
	sentSummary      store.SentStat
	sentWhen         time.Time

	// связь — состояние переподключений: кто рвётся и давно ли его пауза
	// стоит в потолке. В памяти, а не в базе: после перезапуска связь
	// начинается заново, и хранить это незачем
	link linkWatch

	// готовая подпись VAPID для служб пушей: Apple не просит обновлять её
	// чаще раза в час (push.go)
	pushAuth pushAuthCache

	// поиск страниц (`sema.web.find`): очередь пауз и кэш выдачи — общие
	// на весь процесс, в памяти
	find findState

	// слушатели учёток Telegram: у каждой своё соединение MTProto, и
	// остановить надо уметь одну, не трогая соседние
	tgLive      sync.Mutex
	tgListeners map[string]context.CancelFunc
	// клиенты слушателей: по ним, пока соединение живо, идёт разговор с
	// хозяином (tg_talk.go); под тем же замком tgLive
	tgListenCl map[string]*tg.Client

	// разговор с хозяином в Telegram: ритм «печатает» по чатам и очередь
	// ответов; tgVia — подменённый Telegram (в работе всегда nil, в тестах
	// им проверяется порядок вызовов без сети), tgPulse — как часто
	// повторять «печатает» (ноль — как принято у Telegram)
	tgTalkMu     sync.Mutex
	tgPulses     map[string]*tgPulse
	tgAdminLocks map[int64]*sync.Mutex
	tgVia        tgTransport
	tgPulse      time.Duration
	// tgFetch — как забрать вложение из Telegram: в работе nil (настоящее
	// скачивание), в тестах подменяется, чтобы голосовые шли без сети
	tgFetch func(ctx context.Context, c *store.TGChat, f *tg.File) ([]byte, error)

	conf      *config.Config
	store     *store.Store
	providers map[string]llm.Provider
	tokens    mail.TokenSource // nil при входе по паролю
	embedder  *embed.Embedder  // nil — RAG выключен

	mu       sync.Mutex // сериализует IMAP-сеансы
	live     *liveHub   // сигналы «экран устарел» для живого обновления
	pollNow  chan struct{}
	jobs     chan struct{} // сигнал воркеру: в очереди есть подтверждённый спам
	LastPoll time.Time
	LastErr  string

	backfillDone  atomic.Int64
	backfillTotal atomic.Int64

	// разбор всех папок: сколько писем уложено в архив и сколько осталось
	foldersDone atomic.Int64
	foldersLeft atomic.Int64
	// сколько попыток построить вектор подряд не удалось и чем
	vectorsInARow atomic.Int64
	vectorsErr    atomic.Value

	workers    workers     // состояние фоновых процессов
	wakeWorker chan string // «запустить сейчас» для фонового процесса
	// мгновенная доставка событий модулям: один разборщик, флаг «ещё раз»
	eventsMu      sync.Mutex
	eventsRunning bool
	eventsAgain   bool
	eventsPassMu  sync.Mutex   // один разборщик очереди за раз
	polling       atomic.Bool  // идёт проверка почты прямо сейчас
	pollNew       atomic.Int64 // сколько новых писем разобрано в текущем проходе

	runningMu sync.Mutex // задачи, выполняющиеся прямо сейчас
	running   map[int64]bool
	beats     atomic.Int64 // удары сердца
	lastBeat  atomic.Int64 // время последнего удара, мс

	thinkingMu     sync.Mutex // чаты, где Сёма сейчас готовит ответ
	assignSync     bool       // тесты: ход поручения в той же горутине, а не фоном
	tgCommentsBusy sync.Map   // каналы, у которых сейчас читаются комментарии к постам
	thinking       map[int64]bool
	partial        map[int64]string             // уже написанная часть ответа
	partialSkip    map[int64]int                // сколько байт partial — текст прежнего хода, а не нынешнего
	stopChat       map[int64]context.CancelFunc // «Остановить» у думающей Сёмы
	// живые соединения агентов: звонит агент, Сёма отвечает
	linkMu  sync.Mutex
	agentMu sync.Mutex   // изменения прав и объявлений не перезаписывают друг друга
	caMu    sync.Mutex   // корень и сертификат Сёмы заводятся по одному
	memMu   sync.Mutex   // инструменты памяти для провода заводятся по одному
	memSrv  *mcp.Server  // они же: одни на движок, как раньше на сервер MCP
	provMu  sync.Mutex   // справочник провайдеров правится по одному
	prices  priceRun     // проверки цен провайдеров, идущие сейчас (price_watch.go)
	bal     balanceRun   // проверки остатка на счетах, идущие сейчас (balance.go)
	modelMu sync.RWMutex // карта моделей: её читают роли и чаты, а пишет подключение

	// разбор списка моделей и справочника провайдеров стоит дорого
	// (JSON плюс расшифровка полусотни ключей), а экран «Модели»
	// спрашивает их на каждую строку — держим разобранное, пока не
	// сменилась версия ключа в хранилище (store.SettingVersion). Раньше
	// сверялась сама строка: каждый вызов читал из базы 28 КБ ради того,
	// чтобы узнать, что ничего не изменилось (perf-pass 2.3)
	cacheMu    sync.Mutex
	modelsVer  uint64
	modelsOK   bool
	modelsList []ModelConf
	modelsIdx  map[string]int // имя → номер в modelsList (первое вхождение)
	provVer    uint64
	provOK     bool
	provList   []ProviderConf
	// modules.json — 100 КБ вместе с кодом модулей, и «Здоровье» разбирало
	// его на каждую из 59 строк расхода (perf-pass 2.2)
	modVer   uint64
	modCache map[string]*Module
	// engineUp — движок запущен (Run): фоновые проверки машины разрешены
	engineUp atomic.Bool
	savings  map[string]AgentSavings // счёт сэкономленного по агентам
	links    map[string]*link
	// agentOffGen — отметка последнего обрыва или возвращения агента, под
	// linkMu: отложенное уведомление об обрыве сверяет с ней свою
	agentOffGen map[string]uint64

	// шлюз: счётчики своего конца провода. Отдельный замок, а не
	// linkMu: потоки открываются пачкой и не должны ждать экран «Агенты»
	gateMu sync.Mutex
	gate   gatewayStats

	// sshVia — подменённый транспорт SSH: в работе всегда nil, в тестах
	// им проверяется ночная смена целиком, без живого сервера
	sshVia sshTransport

	// серверы MCP: клиенты с живой сессией, начатые входы OAuth и замки
	// продления токенов (mcp_oauth.go). Всё в памяти: сессия и начатый
	// вход не должны переживать перезапуск
	mcpMu sync.Mutex
	mcpX  mcpRuntime
	// перепроверка сервера MCP из чата (mcp_chat.go): под замком, чтобы два
	// чата не ходили на упавший сервер одновременно
	mcpFreshMu sync.Mutex

	// предложения скиллов лежат в базе (skill_drafts), а не здесь
	pendingMu sync.Mutex
	chatNotes map[int64]string     // заметка таймера или пинка для следующего хода модели
	cardQueue map[int64][]ToolCard // карточки ждут, пока Сёма допишет ответ
}

func New(conf *config.Config, st *store.Store) (*Engine, error) {
	providers := map[string]llm.Provider{}
	for name, pc := range conf.LLM.Providers {
		p, err := llm.Build(name, pc)
		if err != nil {
			return nil, err
		}
		providers[name] = p
	}
	// без моделей Сёма тоже поднимается: это не поломка, а ненастроенная
	// система — мастер в приложении доведёт её до рабочего состояния
	e := &Engine{
		conf:       conf,
		store:      st,
		providers:  providers,
		pollNow:    make(chan struct{}, 1),
		jobs:       make(chan struct{}, 1),
		wakeWorker: make(chan string, 4),
		links:      map[string]*link{},
		live:       newLiveHub(),
	}
	// хранилище — единственное место, где меняется показываемое: сигналы
	// об этом идут оттуда, движок только раздаёт их подписчикам
	st.OnChange(func(screens ...string) {
		e.live.changed(screens...)
		// счётчик непрочитанных живёт и на маке: значок у часов гаснет,
		// когда записи разобрали в браузере
		for _, s := range screens {
			if s == store.ScreenNotifications {
				e.PushNotifyCount()
				break
			}
		}
	})
	// разовая починка правил, потерянных из-за сбоя переноса: если делать
	// это при каждом старте, удалённые вручную разрешения воскресали бы
	if st.Setting("ham_rules_synced", "") != "1" {
		if n, err := st.SyncHamRules(); err == nil {
			if n > 0 {
				log.Printf("белый список: восстановлено правил %d", n)
			}
			_ = st.SetSetting("ham_rules_synced", "1")
		}
	}
	// секреты: ключ приходит из окружения и в базе не хранится
	if box, berr := secret.NewFromEnv(os.Getenv(conf.KeyEnvName())); berr == nil && box != nil {
		st.UseSecretBox(box)
		// проверочная запись: ключ не тот — говорим сразу, а не при первом
		// чтении пароля ящика. Ключ похож на фразу с опечаткой — говорим и это
		if ok, known := st.KeyMatches(); known && !ok {
			hint := ""
			if raw := os.Getenv(conf.KeyEnvName()); secret.LooksLikePhrase(raw) {
				if _, perr := secret.ParsePhrase(raw); perr != nil {
					hint = " · " + perr.Error()
				}
			}
			log.Printf("КЛЮЧ НЕ ТОТ: %s не подходит к этой базе — секреты, пароли ящиков и ключи моделей не прочитаются%s", conf.KeyEnvName(), hint)
		} else if err := st.EnsureKeyCheck(); err != nil {
			log.Printf("проверочная запись ключа: %v", err)
		}
		if n, merr := st.EncryptExistingSecrets(); merr != nil {
			log.Printf("шифрование секретов: %v", merr)
		} else if n > 0 {
			log.Printf("секреты зашифрованы: записей %d", n)
		}
		e.encryptModelKeys()
	} else if berr != nil {
		log.Printf("ключ шифрования: %v", berr)
	}
	// Адрес и модель эмбеддера — из настроек, файл запуска только
	// первичное значение. До 10 сентября 2026 здесь стоял один файл:
	// человек менял адрес на «Инструментах», SetEmbeddings поднимал
	// нового клиента, а следующий же перезапуск возвращал адрес из
	// config.yaml — и молча. На живой Сёме это значило, что векторов не
	// строилось вовсе: в журнале тысячи строк «lookup embed: no such
	// host», а поиск по смыслу не работал месяцами. Правило то же, что
	// со справочниками: настройка сильнее файла запуска.
	if base := e.store.Setting(embedURLKey, conf.Embeddings.BaseURL); base != "" {
		model := e.store.Setting(embedModelKey, conf.Embeddings.Model)
		e.embedder = embed.New(hostAlias(base), model, conf.Embeddings.Key())
	}
	e.importSeed(conf) // разовый перенос файла в базу
	for name := range e.removedModels() {
		e.dropProvider(name) // удалённую модель файл запуска не возвращает
	}
	e.moveKeysToProviders() // разовый переезд ключей моделей к провайдерам
	e.migratePricePage()    // страница цен: одна на всех → у провайдера
	e.fixPeerMessages()     // разовая починка старых реплик собеседника
	e.fixNoteMessages()     // и служебных вводных, стоявших вашей репликой
	e.loadUserModels()
	e.ensureSelfModule() // встроенный модуль «Сёма» с общей инструкцией
	e.ensureMemorySeed() // области, маски секретов, типы графа, потребитель «Сёма-чат»
	e.MergeNestedConsumers()
	e.loadBuiltinModules() // модули из образа: новые выключены, изменившиеся обновлены
	e.loadBuiltinSkills()  // скиллы из образа: новые включены, изменившиеся обновлены, если человек не правил
	e.ApplyTimezone(e.store.Setting("app.timezone", ""))
	e.VerifyToggles()           // настройка «включено» без включённой задачи = копии нет
	e.MigrateAccountPasswords() // пароли ящиков из настройки в хранилище секретов
	e.FixImageNames()           // «Файл «bf62b2db…»» вместо имени в вопросах про картинки
	e.repairBodies()            // тексты писем без начинки комментариев и набивки
	e.repairSnippets()          // превью писем без MIME-мусора: он уходил и модели
	e.ensureNotifySeed()        // правила почты и канал Mac
	e.migrateAttentionModule()  // напоминания модуля «Требует внимания» → уведомления ядра
	e.migrateMCPCircles()       // круги серверов MCP, поставленные первым заполнением, — по границам слов
	e.migrateMCPRules()         // круги вызовов → правила по инструментам: разрешить, спросить, запретить
	// чеки модуля «Чеки» → на сами файлы и выпиской в «Файлы»; модуль снят
	e.MigrateReceiptsModule(context.Background())
	// письма, уехавшие в архив мимо разбора, пока обход папок забирал
	// «Входящие» раньше него (см. rejudge.go)
	// уборка за первым, слишком широким досудом — до самого досуда
	e.CancelWidePreCourt()
	e.FixServiceReason()
	e.RejudgeUnjudged()
	// указатель поиска по письмам: без него поиск отвечает «ничего не
	// нашлось» и выглядит сломанным. Отдельной ниткой — старт не ждёт
	go e.FillCursor()
	// написания поставщиков в справочник: до него суммы по поставщику
	// врали — «Горэнерго» стоял тремя строками
	e.ConsolidateProviders()
	// задачи включённых модулей: название и расписание — по манифесту
	for _, m := range e.Modules() {
		if m.Enabled {
			if err := e.syncModuleTasks(m); err != nil {
				log.Printf("задачи модуля %s: %v", m.Name, err)
			}
		}
	}
	e.ensureRuleSets() // наборы правил «Linux» и «RouterOS» для серверов без правил
	if conf.Mail.Auth == "gmail" {
		oauth := &google.OAuth{
			ClientID:     conf.Mail.Google.ClientID,
			ClientSecret: conf.Mail.Google.Secret(),
			Store:        st.Secrets(), // токен обновления хранится закрытым
		}
		e.tokens = oauth.AccessToken
	}
	// письма от адресов самих ящиков, прочитанные из папок, — исходящие
	var own []string
	for _, a := range e.Accounts() {
		own = append(own, a.Username, a.Name)
	}
	st.MarkOutgoing(own)
	e.installUsage() // провайдеры пишут расход через llm.OnCall
	// разовый перенос: модели по своим адресам становятся доверенными, как
	// считались до появления признака; дальше доверие ставит человек
	e.seedModelTrust()
	e.seedEmbedTrust()
	if note := e.bindingStatus(); note != "" {
		log.Printf("НУЖНА ПРИВЯЗКА: Сёма поднят из копии (%s) — почта, папки и переносы не запускаются, пока привязка не подтверждена на «Здоровье»", note)
	}
	return e, nil
}

// ProviderNames — для формы настроек в UI.
// provider — модель по имени. Карту читают роли, чаты и разбор писем, а
// пишет подключение провайдера: без замка это «concurrent map read and
// map write», то есть падение всего сервера.
// provider — модель по имени, обёрнутая дверью. Обёртка здесь, а не у
// каждого вызывающего, ровно потому, что путей к модели много и новый
// обязательно забудут проверить: чат, роль, модуль, разметка спама,
// картинки, поиск. Дверь у выхода стоит один раз и покрывает все.
func (e *Engine) provider(name string) llm.Provider {
	e.modelMu.RLock()
	p := e.providers[name]
	e.modelMu.RUnlock()
	if p == nil {
		return nil
	}
	return &gate{Provider: p, e: e, name: name}
}

func (e *Engine) hasProvider(name string) bool {
	e.modelMu.RLock()
	defer e.modelMu.RUnlock()
	_, ok := e.providers[name]
	return ok
}

func (e *Engine) setProvider(name string, p llm.Provider) {
	e.modelMu.Lock()
	defer e.modelMu.Unlock()
	e.providers[name] = p
}

func (e *Engine) dropProvider(name string) {
	e.modelMu.Lock()
	defer e.modelMu.Unlock()
	delete(e.providers, name)
}

// ProviderNames — имена моделей в устойчивом порядке: списки в интерфейсе
// не должны перетасовываться от обновления к обновлению.
func (e *Engine) ProviderNames() []string {
	e.modelMu.RLock()
	names := make([]string, 0, len(e.providers))
	for n := range e.providers {
		names = append(names, n)
	}
	e.modelMu.RUnlock()
	sort.Strings(names)
	return names
}

func (e *Engine) ActiveProvider() string {
	name := e.store.Setting("llm.active", e.conf.LLM.Active)
	if e.hasProvider(name) {
		return name
	}
	// на свежей установке активной ещё нет — берём единственную подключённую
	if names := e.ProviderNames(); len(names) > 0 {
		return names[0]
	}
	return name
}

func (e *Engine) SetActiveProvider(name string) error {
	if !e.hasProvider(name) {
		return fmt.Errorf("unknown provider %q", name)
	}
	return e.store.SetSetting("llm.active", name)
}

// Run запускает перенос спама и сердцебиение — единственный планировщик,
// от которого работают все остальные задачи.
func (e *Engine) Run(ctx context.Context) {
	go e.moveWorker(ctx)
	// своё распознавание картинок проверяется сразу, в фоне: на маке это
	// сборка помощника, и иначе её ждал бы первый экран «Настроек»
	e.engineUp.Store(true)
	go systemOCR(e.conf.Files.Dir)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.pollNow:
				if e.NeedsBinding() {
					log.Printf("почта: не забираю — Сёма ждёт привязки (подтвердите на «Здоровье»)")
					continue
				}
				e.poll(ctx)
			}
		}
	}()
	e.heartbeat(ctx)
}

// purgeAll — чистка почтового сервера по всем ящикам, по одному дню за
// проход. Что именно стёрто, пишется в журнал удалений: у Сёмы копия
// остаётся навсегда, но человек вправе знать, чего больше нет на сервере.
func (e *Engine) purgeAll(ctx context.Context) int {
	n := 0
	var reports []string
	for _, acct := range e.Accounts() {
		if e.MailboxReadOnly(acct.Name) {
			continue // на сервере такого ящика ничего не удаляем
		}
		sess, err := e.dialAccount(acct)
		if err != nil {
			log.Printf("purge dial %s: %v", acct.Name, err)
			continue
		}
		erased, report := e.DailyPurge(sess, acct.Name)
		n += erased
		if report != "" {
			reports = append(reports, acct.Name+" · "+report)
		}
		sess.Close()
	}
	if len(reports) > 0 {
		log.Printf("чистка почты: %s", strings.Join(reports, "; "))
	}
	return n
}

// moveWorker в фоне переносит подтверждённый спам (status=queued) в Junk —
// клик в UI мгновенный, IMAP работает отдельно. Один сеанс на пачку.
func (e *Engine) moveWorker(ctx context.Context) {
	// на старте добираем то, что осталось в очереди с прошлого запуска —
	// кроме копии, ждущей привязки: перенос в спам с копии был бы вторым
	if !e.NeedsBinding() {
		e.processQueued()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.jobs:
			if !e.NeedsBinding() {
				e.processQueued()
			}
		}
	}
}

func (e *Engine) processQueued() {
	e.mu.Lock()
	defer e.mu.Unlock()
	queued, err := e.store.ByStatus(store.StatusQueued, nil, 500)
	if err != nil || len(queued) == 0 {
		return
	}
	byAcct := map[string][]*store.Message{}
	for _, m := range queued {
		name := m.Account
		if name == "" {
			name = e.conf.Mail.Name
		}
		byAcct[name] = append(byAcct[name], m)
	}
	for name, msgs := range byAcct {
		e.moveBatch(name, msgs)
	}
}

func (e *Engine) moveBatch(account string, queued []*store.Message) {
	acct, ok := e.accountByName(account)
	if !ok {
		log.Printf("move worker: unknown account %q", account)
		return
	}
	if e.MailboxReadOnly(account) {
		// «только читать»: решение человека запоминаем, письмо на сервере не трогаем
		for _, m := range queued {
			now := time.Now()
			_ = e.store.SetStatus(m.ID, store.StatusConfirmed, &now)
			_ = e.store.AddRule("sender", m.FromAddr, "spam")
			e.applySpammerRule(m.FromAddr)
		}
		return
	}
	sess, err := e.dialAccount(acct)
	if err != nil {
		e.LastErr = err.Error()
		log.Printf("move worker: %v", err)
		return
	}
	defer sess.Close()
	for _, m := range queued {
		// перенос на почтовом сервере — внешнее действие: повторить его
		// нельзя, а прошлый заход мог умереть между переносом и записью
		// результата. Ключ — само письмо: UID меняется, Message-ID нет
		key := "mail-move:" + m.MessageID
		if retry, _ := e.store.OutsideStarted(key); retry {
			if e.store.OutsideState(key) == "done" {
				// перенос уже случился, не дошла только запись — дописываем
				now := time.Now()
				_ = e.store.SetStatus(m.ID, store.StatusConfirmed, &now)
				log.Printf("move: письмо %q уже перенесено прошлым заходом — дописал результат", m.Subject)
				continue
			}
			log.Printf("move: прошлый заход умер посреди переноса %q — отдаю человеку", m.Subject)
			_ = e.store.SetStatus(m.ID, store.StatusPending, nil)
			continue
		}
		moved, err := sess.MoveToJunkByID(m.MessageID, m.UID)
		if err != nil {
			log.Printf("move %q to %q: %v", m.Subject, sess.JunkFolder(), err)
			e.LastErr = "перенос в спам: " + err.Error()
			_ = e.store.SetStatus(m.ID, store.StatusPending, nil)
			_ = e.store.ForgetOutside(key) // до почты не дошли — повтор безопасен
			continue
		}
		_ = e.store.OutsideDone(key)
		if !moved {
			// письма нет ни во «Входящих», ни в спаме — оно удалено вручную
			log.Printf("move: письмо %q не найдено в почте", m.Subject)
		}
		now := time.Now()
		_ = e.store.SetStatus(m.ID, store.StatusConfirmed, &now)
		_ = e.store.AddRule("sender", m.FromAddr, "spam")
		e.applySpammerRule(m.FromAddr)
	}
}

func (e *Engine) TriggerPoll() {
	select {
	case e.pollNow <- struct{}{}:
	default:
	}
}

func (e *Engine) poll(ctx context.Context) {
	e.polling.Store(true)
	e.pollNew.Store(0)
	defer e.polling.Store(false)

	e.mu.Lock()
	defer e.mu.Unlock()
	e.LastPoll = time.Now()
	e.LastErr = ""
	if err := e.pollLocked(ctx); err != nil {
		e.LastErr = err.Error()
		log.Printf("poll: %v", err)
	}
}

func (e *Engine) pollLocked(ctx context.Context) error {
	e.backfillEmbeddings(ctx)
	var firstErr error
	for _, acct := range e.Accounts() {
		if err := e.pollAccount(ctx, acct); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", acct.Name, err)
		}
	}
	return firstErr
}

func (e *Engine) pollAccount(ctx context.Context, acct config.Mail) error {
	sess, err := e.dialAccount(acct)
	if err != nil {
		return err
	}
	defer sess.Close()
	return e.scanInbox(ctx, sess, acct.Name)
}

func (e *Engine) scanInbox(ctx context.Context, sess *mail.Session, account string) error {
	msgs, err := sess.FetchInbox()
	if err != nil {
		return err
	}
	rules, err := e.store.Rules()
	if err != nil {
		return err
	}
	// «только читать»: письма забираем, но не судим и не трогаем на сервере
	readOnly := e.MailboxReadOnly(account)
	for _, f := range msgs {
		if err := e.processFetched(ctx, sess, f, rules, account, readOnly); err != nil {
			return err
		}
	}
	return nil
}

// processFetched проводит одно письмо через весь конвейер: дедупликация,
// правила, RAG-похожесть, LLM, эмбеддинг.
func (e *Engine) processFetched(ctx context.Context, sess *mail.Session, f *mail.Fetched, rules []*store.Rule, account string, readOnly bool) error {
	known, err := e.store.KnownMessage(f.MessageID)
	if err != nil || known {
		return err
	}
	m := &store.Message{
		Account:   account,
		MessageID: f.MessageID,
		UID:       f.UID,
		FromAddr:  strings.ToLower(f.FromAddr),
		FromName:  f.FromName,
		Subject:   f.Subject,
		Date:      f.Date,
		Snippet:   f.Snippet,
		InReplyTo: strings.Join(f.InReplyTo, " "),
		Refs:      strings.Join(f.Refs, " "),
		ReplyTo:   f.ReplyTo,
		To:        strings.Join(f.To, " "),
		Cc:        strings.Join(f.Cc, " "),
		ListUnsub: f.ListUnsub,
	}

	// вектор нужен и для RAG-решения, и для будущего поиска
	var vec []float32
	if e.embedder != nil {
		var eerr error
		vec, eerr = e.embedIfAllowed(ctx, embText(m.FromAddr, m.Subject, m.Snippet))
		if eerr != nil {
			log.Printf("embed %q: %v", m.Subject, eerr)
		}
	}

	rule := matchRule(rules, m.FromAddr)
	if readOnly {
		m.Status = store.StatusInboxOK
		m.Reason = "ящик в режиме «только читать»"
		rule = "handled"
	}
	if rule == "" && e.WhitelistOnly() {
		// счёт от нового поставщика — обычное дело, и белый список о нём
		// знать не обязан. Проверяем ДО переноса: после него «письмо ждёт
		// решения» было бы неправдой — оно уже в спам-корзине сервера
		if e.holdForMoney(m, "отправитель не в списке разрешённых") {
			rule = "handled"
		} else if _, err := sess.MoveToJunkByID(f.MessageID, f.UID); err != nil {
			// режим белого списка: незнакомый отправитель — сразу в спам
			log.Printf("whitelist move uid %d: %v", f.UID, err)
			rule = "" // не получилось — пусть решает модель
		} else {
			now := time.Now()
			m.Status = store.StatusAutoMoved
			m.MovedAt = &now
			m.Confidence = 1
			m.Reason = "отправитель не в списке разрешённых"
			rule = "handled"
		}
	}
	switch rule {
	case "handled":
		// уже перенесено выше
	case "ham":
		m.Status = store.StatusHam
		m.Reason = "правило: отправитель в белом списке"
	case "spam":
		// похожее письмо уже подтверждали — переносим сразу. Но не когда в
		// письме виден счёт: рассылка и квитанция приходят с одного адреса,
		// а правило про адрес (см. money.go)
		if e.holdForMoney(m, "правило: отправитель уже подтверждён как спамер") {
			break
		}
		if _, err := sess.MoveToJunkByID(f.MessageID, f.UID); err != nil {
			log.Printf("auto-move uid %d: %v", f.UID, err)
			m.Status = store.StatusPending
			m.Reason = "правило: спам-отправитель (перенос не удался, нужен ручной)"
		} else {
			now := time.Now()
			m.Status = store.StatusAutoMoved
			m.MovedAt = &now
			m.Reason = "правило: отправитель уже подтверждён как спамер"
			m.Confidence = 1
		}
	default:
		// сначала пробуем решить по смысловой похожести на размеченное
		if !e.ragDecide(sess, m, vec) {
			e.classify(ctx, e.filterModel(m), m, e.ragExamples(vec))
		}
	}
	if err := e.store.InsertMessage(m); err != nil {
		return err
	}
	if vec != nil {
		if err := e.store.SaveEmbedding(m.ID, embed.EncodeVec(vec)); err != nil {
			log.Printf("save embedding %d: %v", m.ID, err)
		}
	}
	e.emitModuleEvent(EventMessageAdded, m.ID)
	e.EnqueueTranslate(m.ID)
	e.pollNew.Add(1)
	if m.Status == store.StatusPending {
		log.Printf("suspected spam: %s — %q (%.0f%%)", m.FromAddr, m.Subject, m.Confidence*100)
	}
	return nil
}

func (e *Engine) classify(ctx context.Context, p llm.Provider, m *store.Message, examples string) {
	if err := e.budgetCheck(ctx); err != nil {
		m.Status = store.StatusInboxOK
		m.Reason = "фильтр не работал: " + err.Error()
		return
	}
	if p == nil {
		m.Status = store.StatusInboxOK
		if m.Reason == "" {
			// причину мог поставить filterModel: «домашней модели нет»
			m.Reason = "LLM-провайдер не настроен, письмо пропущено"
		}
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// дверь должна знать, о каком письме речь: пометка человека стоит на
	// письме, а в промпте разметки одна тема и выжимка
	cctx = llm.WithSubject(cctx, store.ClassMsg, m.ID)
	v, err := llm.Classify(cctx, p, llm.Email{From: m.FromAddr, Subject: m.Subject, Snippet: m.Snippet}, examples)
	if err != nil {
		log.Printf("classify %q: %v", m.Subject, err)
		m.Status = store.StatusInboxOK
		m.Reason = "ошибка классификации: " + err.Error()
		return
	}
	m.Confidence = v.Confidence
	m.Reason = v.Reason
	if v.Spam {
		m.Status = store.StatusPending
	} else {
		m.Status = store.StatusInboxOK
	}
}

func matchRule(rules []*store.Rule, fromAddr string) string {
	domain := ""
	if i := strings.LastIndex(fromAddr, "@"); i >= 0 {
		domain = fromAddr[i+1:]
	}
	for _, r := range rules {
		if (r.Kind == "sender" && r.Value == fromAddr) || (r.Kind == "domain" && r.Value == domain) {
			return r.Action
		}
	}
	return ""
}

// purgeJunk удаляет с почтового сервера то, что Сёма перенёс в спам
// больше junk_retention_days назад. Перед удалением письмо целиком
// сохраняется в локальный архив: из памяти Сёмы письма не пропадают
// никогда — удаление касается только почтового ящика.
func (e *Engine) purgeJunk(sess *mail.Session, account string) error {
	cutoff := time.Now().AddDate(0, 0, -e.RetentionDays())
	old, err := e.store.MovedBefore(cutoff)
	if err != nil {
		return err
	}
	for _, m := range old {
		if m.Account != account && !(m.Account == "" && account == e.conf.Mail.Name) {
			continue
		}
		if err := e.archiveBeforePurge(sess, m); err != nil {
			log.Printf("archive %s: %v — удаление отложено", m.MessageID, err)
			continue // без локальной копии не удаляем
		}
		found, err := sess.DeleteFromJunk(m.MessageID)
		if err != nil {
			log.Printf("purge %s: %v", m.MessageID, err)
			continue
		}
		if !found {
			log.Printf("purge: %s уже нет в спаме", m.MessageID)
		}
		if err := e.store.SetStatus(m.ID, store.StatusPurged, nil); err != nil {
			return err
		}
	}
	return nil
}

// archiveBeforePurge гарантирует, что текст и вложения письма лежат
// локально, прежде чем оно исчезнет с сервера.
func (e *Engine) archiveBeforePurge(sess *mail.Session, m *store.Message) error {
	bodies, err := e.store.Bodies([]int64{m.ID})
	if err != nil {
		return err
	}
	if b, ok := bodies[m.ID]; ok && (b.HTML != "" || len(b.Text) > 200) {
		return nil // копия уже есть
	}
	uid, ok := sess.FindUIDByMessageID(sess.JunkFolder(), m.MessageID)
	if !ok {
		// письма уже нет на сервере — сохраняем хотя бы то, что знаем
		return e.store.SaveBody(m.ID, store.Body{Text: mail.Sanitize(m.Snippet)})
	}
	texts, err := sess.FetchRichIn(sess.JunkFolder(), []uint32{uid})
	if err != nil {
		return err
	}
	rb := texts[uid]
	if rb.Text == "" && rb.HTML == "" {
		rb.Text = mail.Sanitize(m.Snippet)
	}
	return e.store.SaveBody(m.ID, store.Body{Text: rb.Text, HTML: rb.HTML})
}

// ConfirmSpam — пользователь подтвердил спам: мгновенно ставим в очередь,
// фоновой воркер перенесёт в Junk и запомнит отправителей.
// применитьПравилоСпамера — правило «этот отправитель спамер» только что
// появилось (человек подтвердил спам), а другие его письма всё ещё ждут
// решения в «Похоже на спам — подтвердите». Они уезжают в спам тем же
// решением; письмо со счётом остаётся ждать — про деньги правило по
// адресу не решает (money.go). 13 сентября 2026: «уже несколько раз
// отправлял в спам, а отправитель всё просится» — просились как раз
// соседние письма того же адреса.
func (e *Engine) applySpammerRule(addr string) int {
	wait, err := e.store.PendingFromSender(addr, 0)
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range wait {
		if e.holdForMoney(m, "правило: отправитель уже подтверждён как спамер") {
			continue
		}
		if err := e.store.SetStatus(m.ID, store.StatusQueued, nil); err == nil {
			n++
		}
	}
	if n > 0 {
		select {
		case e.jobs <- struct{}{}:
		default:
		}
	}
	return n
}

// ServerSpamVotes — после скольких писем в спаме у провайдера адрес
// становится спамером и у Сёмы; 0 — не учиться.
func (e *Engine) ServerSpamVotes() int {
	// счёт, а не «положительная»: ноль здесь — честное «выключено»
	return e.store.SettingInt(spamServerVotesDef)
}

// spamServerVotesKey — настройка порога; умолчание и границы — в реестре
// (setting_defs.go), потому что она стоит в «Тонкой настройке» вкладки «Спам».
const spamServerVotesKey = "spam.server_votes"

func (e *Engine) SetServerSpamVotes(n int) error {
	if n < 0 || n > 50 {
		return fmt.Errorf("порог должен быть от 0 до 50")
	}
	return e.store.SetSetting(spamServerVotesKey, strconv.Itoa(n))
}

// learnFromServerSpam — письмо легло в папку спама у провайдера: решение
// принял его фильтр или сам человек в почтовом клиенте. Одно такое
// письмо ничего не значит, но `spam.server_votes` писем одного адреса
// подряд — это уже отправитель, которого человек не ждёт: он становится
// спамером и у Сёмы, а его письма, ждущие решения, уезжают. Белый список
// сильнее: адрес с правилом «не спам» так не учится. Корзина не считается
// — удалить рассылку не значит назвать её спамом.
func (e *Engine) learnFromServerSpam(addr string) {
	if addr == "" {
		return
	}
	threshold := e.ServerSpamVotes()
	if threshold <= 0 {
		return
	}
	n, err := e.store.CountFromSenderWithStatus(addr, store.StatusServerSpam)
	if err != nil || n < threshold {
		return
	}
	rules, err := e.store.Rules()
	if err != nil || matchRule(rules, addr) != "" {
		return
	}
	if err := e.store.AddRule("sender", addr, "spam"); err != nil {
		return
	}
	log.Printf("спам: %s %d раз лежал в спаме у провайдера — теперь спамер и у Сёмы, ждущих писем уехало %d",
		addr, n, e.applySpammerRule(addr))
}

func (e *Engine) ConfirmSpam(ids []int64) error {
	for _, id := range ids {
		if err := e.store.SetStatus(id, store.StatusQueued, nil); err != nil {
			return err
		}
	}
	select {
	case e.jobs <- struct{}{}:
	default:
	}
	return nil
}

// MarkHam — «не спам»: запоминаем отправителей в белый список.
func (e *Engine) MarkHam(ids []int64) error {
	for _, id := range ids {
		m, err := e.store.Message(id)
		if err != nil {
			return err
		}
		if err := e.store.SetStatus(id, store.StatusHam, nil); err != nil {
			return err
		}
		if err := e.store.AddRule("sender", m.FromAddr, "ham"); err != nil {
			return err
		}
	}
	return nil
}

// MigrateAccountPasswords — разовый переезд паролей ящиков из настройки
// в хранилище секретов. Молча не проходит: если хранилище отказало (нет
// ключа шифрования), об этом надо знать — пароль так и лежит открытым.
func (e *Engine) MigrateAccountPasswords() {
	n, err := e.store.MigrateAccountPasswords()
	if err != nil {
		log.Printf("ВНИМАНИЕ: пароль ящика остался в настройке открытым текстом: %v", err)
		return
	}
	if n > 0 {
		log.Printf("пароли ящиков переехали в хранилище секретов: %d", n)
	}
}

func (e *Engine) Store() *store.Store { return e.store }

// ReadServerFolders — читать ли папки, где решение принял почтовый
// сервер или человек: спам провайдера, корзину и «всю почту» Gmail.
//
// Включено по умолчанию с 10 сентября 2026. До этого «письмо не
// нашлось» у Сёмы значило «письма нет», хотя оно лежало в спаме у
// провайдера. Выключатель оставлен: письма из архива Сёмы не удаляются
// никогда, и решение забирать к себе чужой спам должно быть отменяемым
// хотя бы на будущее.
func (e *Engine) ReadServerFolders() bool {
	return e.store.Setting("mail.read_server_folders", "1") == "1"
}

func (e *Engine) SetReadServerFolders(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting("mail.read_server_folders", v)
}

// WhitelistOnly — во «Входящих» остаются только письма от разрешённых
// отправителей, всё остальное сразу уезжает в спам (модель не нужна).
func (e *Engine) WhitelistOnly() bool {
	return e.store.Setting("mail.whitelist_only", "") == "1"
}

func (e *Engine) SetWhitelistOnly(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return e.store.SetSetting("mail.whitelist_only", v)
}

// CleanInbox переносит в спам всё, что лежит во «Входящих» не от
// разрешённых отправителей. Копии писем и файлы остаются у Сёмы.
func (e *Engine) CleanInbox() (int, error) {
	msgs, err := e.store.ByStatus(store.StatusInboxOK, nil, 2000)
	if err != nil {
		return 0, err
	}
	rules, err := e.store.Rules()
	if err != nil {
		return 0, err
	}
	moved := 0
	readOnly := e.readOnlyAccounts()
	for _, m := range msgs {
		if matchRule(rules, m.FromAddr) == "ham" || readOnly[m.Account] {
			continue // отправитель разрешён или ящик только читаем — письмо остаётся
		}
		if err := e.store.SetStatus(m.ID, store.StatusQueued, nil); err != nil {
			return moved, err
		}
		moved++
	}
	if moved > 0 {
		select {
		case e.jobs <- struct{}{}:
		default:
		}
	}
	return moved, nil
}

// InboxCleanupCount — сколько писем уедет при очистке.
func (e *Engine) InboxCleanupCount() int {
	msgs, err := e.store.ByStatus(store.StatusInboxOK, nil, 2000)
	if err != nil {
		return 0
	}
	rules, err := e.store.Rules()
	if err != nil {
		return 0
	}
	n := 0
	readOnly := e.readOnlyAccounts()
	for _, m := range msgs {
		if matchRule(rules, m.FromAddr) != "ham" && !readOnly[m.Account] {
			n++
		}
	}
	return n
}

// readOnlyAccounts — ящики в режиме «только читать», по имени.
func (e *Engine) readOnlyAccounts() map[string]bool {
	out := map[string]bool{}
	for _, a := range e.Accounts() {
		if e.MailboxReadOnly(a.Name) {
			out[a.Name] = true
		}
	}
	return out
}

// Пороги настраиваются из UI и живут в БД; конфиг — значение по умолчанию.
func (e *Engine) RetentionDays() int {
	if v, err := strconv.Atoi(e.store.Setting("junk.retention_days", "")); err == nil && v > 0 {
		return v
	}
	return e.conf.Mail.JunkRetentionDay
}

func (e *Engine) SetRetentionDays(v int) error {
	if v < 1 || v > 365 {
		return fmt.Errorf("срок хранения — от 1 до 365 дней")
	}
	return e.store.SetSetting("junk.retention_days", strconv.Itoa(v))
}

func (e *Engine) AutoThreshold() float64 {
	if v, err := strconv.ParseFloat(e.store.Setting("rag.auto_threshold", ""), 64); err == nil && v > 0 {
		return v
	}
	return e.conf.RAG.AutoThreshold
}

func (e *Engine) SetAutoThreshold(v float64) error {
	if v < 0.5 || v > 1 {
		return fmt.Errorf("порог похожести — от 50%% до 100%%")
	}
	return e.store.SetSetting("rag.auto_threshold", strconv.FormatFloat(v, 'f', 2, 64))
}

// Tokens — источник OAuth-токенов (nil при входе по паролю); для CLI-утилит.
func (e *Engine) Tokens() mail.TokenSource { return e.tokens }

var reSubjPrefix = regexp.MustCompile(`(?i)^((re|fwd|fw)\s*:\s*)+`)

// ThreadView — письмо и вся его цепочка с полными текстами (для разворота
// на месте, как в Mail). Тексты качаются одним IMAP-сеансом; если сеанс не
// удался, показываем сниппеты.
func (e *Engine) ThreadView(id int64, want map[int64]bool) (*store.Message, []*store.Message, map[int64]store.Body, error) {
	m, err := e.store.Message(id)
	if err != nil {
		return nil, nil, nil, err
	}
	norm := strings.TrimSpace(reSubjPrefix.ReplaceAllString(m.Subject, ""))
	thread, err := e.store.Thread(norm, 20)
	if err != nil || len(thread) == 0 {
		thread = []*store.Message{m}
	}

	// тела нужны только для развёрнутых писем — остальное клиенту не шлём
	if want == nil {
		want = map[int64]bool{}
	}
	want[m.ID] = true
	var ids []int64
	for _, tm := range thread {
		if want[tm.ID] {
			ids = append(ids, tm.ID)
		}
	}
	bodies, err := e.store.Bodies(ids)
	if err != nil {
		bodies = map[int64]store.Body{}
	}
	for id, b := range bodies {
		if b.HTML != "" {
			b.HTML = mail.SanitizeHTML(b.HTML) // копии могли сохраняться по старым правилам
			bodies[id] = b
		}
	}
	for _, tm := range thread {
		if !want[tm.ID] {
			continue
		}
		if _, ok := bodies[tm.ID]; !ok {
			bodies[tm.ID] = store.Body{Text: mail.Sanitize(tm.Snippet)} // запасной вариант
		}
	}

	normAcct := func(a string) string {
		if a == "" {
			return e.conf.Mail.Name
		}
		return a
	}
	var missing []*store.Message
	for _, tm := range thread {
		if !want[tm.ID] {
			continue
		}
		b := bodies[tm.ID]
		if b.HTML == "" && normAcct(tm.Account) == normAcct(m.Account) {
			missing = append(missing, tm)
		}
	}
	if len(missing) > 0 {
		name := normAcct(m.Account)
		if acct, ok := e.accountByName(name); ok {
			// отдельное IMAP-соединение: не ждём глобальную блокировку
			if sess, err := e.dialAccount(acct); err != nil {
				log.Printf("thread dial: %v", err)
			} else {
				defer sess.Close()
				save := func(id int64, rb mail.RichBody) {
					b := store.Body{Text: rb.Text, HTML: rb.HTML}
					if b.Text == "" {
						b.Text = bodies[id].Text
					}
					bodies[id] = b
					_ = e.store.SaveBody(id, b) // локальная копия навсегда
				}
				// письмо могло переехать в любую папку — ищем по всему ящику
				byBox := map[string]map[uint32]int64{}
				for _, tm := range missing {
					prefer := []string{"INBOX", sess.JunkFolder()}
					switch tm.Status {
					case store.StatusConfirmed, store.StatusAutoMoved, store.StatusQueued:
						prefer = []string{sess.JunkFolder(), "INBOX"}
					}
					box, uid, ok := sess.Locate(tm.MessageID, prefer...)
					if !ok {
						continue
					}
					if byBox[box] == nil {
						byBox[box] = map[uint32]int64{}
					}
					byBox[box][uid] = tm.ID
				}
				for box, ids := range byBox {
					uids := make([]uint32, 0, len(ids))
					for uid := range ids {
						uids = append(uids, uid)
					}
					texts, err := sess.FetchRichIn(box, uids)
					if err != nil {
						log.Printf("thread bodies (%s): %v", box, err)
						continue
					}
					for uid, rb := range texts {
						save(ids[uid], rb)
					}
				}
			}
		}
	}
	return m, thread, bodies, nil
}

// RestoreSender возвращает отправителя из спама: снимает правило, переносит
// его письма из папки спама во «Входящие» и заносит адрес в белый список.
// Локальные копии писем при этом не трогаются — архив Сёмы вечен.
func (e *Engine) RestoreSender(addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return fmt.Errorf("не указан отправитель")
	}
	if err := e.store.DeleteRule("sender", addr); err != nil {
		return err
	}
	// сначала разрешаем отправителя, потом двигаем письма: если почта
	// отвалится на середине, разрешение всё равно сохранится
	if err := e.store.AddRule("sender", addr, "ham"); err != nil {
		return err
	}
	msgs, err := e.store.MessagesFrom(addr, nil, 500)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	sessions := map[string]*mail.Session{}
	defer func() {
		for _, s := range sessions {
			s.Close()
		}
	}()
	restored := 0
	for _, m := range msgs {
		if m.Status != store.StatusConfirmed && m.Status != store.StatusAutoMoved && m.Status != store.StatusQueued {
			continue
		}
		name := m.Account
		if name == "" {
			name = e.conf.Mail.Name
		}
		sess, ok := sessions[name]
		if !ok {
			acct, found := e.accountByName(name)
			if !found {
				continue
			}
			s, err := e.dialAccount(acct)
			if err != nil {
				return err
			}
			sessions[name], sess = s, s
		}
		if moved, err := sess.MoveFromJunk(m.MessageID); err != nil {
			log.Printf("restore %s: %v", m.MessageID, err)
			continue
		} else if moved {
			restored++
		}
		if err := e.store.SetStatus(m.ID, store.StatusHam, nil); err != nil {
			return err
		}
	}
	log.Printf("restore sender %s: возвращено писем %d", addr, restored)
	return nil
}

// ForgetSender убирает адрес из белого списка. Письма этого отправителя
// перестают числиться «не спамом», иначе разрешение вернулось бы само.
func (e *Engine) ForgetSender(addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if err := e.store.DeleteRule("sender", addr); err != nil {
		return err
	}
	return e.store.ClearHamStatus(addr)
}

// PollState — идёт ли проверка почты и сколько новых писем уже разобрано.
func (e *Engine) PollState() (bool, int64) {
	return e.polling.Load(), e.pollNew.Load()
}

// RestoreSendersWithReceipts возвращает из спама всех отправителей, чьи
// письма содержат чеки: квитанции и счета спамом не бывают.
func (e *Engine) RestoreSendersWithReceipts() (int, error) {
	withReceipts := e.SendersWithReceipts()
	rules, err := e.store.Rules()
	if err != nil {
		return 0, err
	}
	restored := 0
	for _, r := range rules {
		if r.Kind != "sender" || r.Action != "spam" {
			continue
		}
		if withReceipts[r.Value] == 0 {
			continue
		}
		if err := e.RestoreSender(r.Value); err != nil {
			log.Printf("restore %s: %v", r.Value, err)
			continue
		}
		restored++
	}
	return restored, nil
}

// SendersWithReceipts — у кого из отправителей есть чеки: считаются файлы
// с видом «чек» у писем этого адреса.
func (e *Engine) SendersWithReceipts() map[string]int {
	out, err := e.store.SendersWithReceipts()
	if err != nil {
		log.Printf("отправители с чеками: %v", err)
	}
	return out
}

// secretKeyEnv — переменная окружения с ключом шифрования секретов.
const secretKeyEnv = "SEMA_KEY"

// encryptModelKeys перечитывает и пересохраняет список моделей, чтобы ключи
// в нём оказались закрытыми.
func (e *Engine) encryptModelKeys() {
	list := e.userModels()
	if len(list) == 0 {
		return
	}
	if err := e.saveUserModels(list); err != nil {
		log.Printf("шифрование ключей моделей: %v", err)
	}
}

// hostAlias подменяет localhost в адресах моделей на имя из SEMA_HOST_ALIAS:
// внутри Docker «127.0.0.1» — это сам контейнер, а LM Studio и Ollama живут
// на хозяине под именем host.docker.internal. Настройки при этом остаются
// как есть — подмена только в момент обращения.
func hostAlias(rawURL string) string {
	alias := os.Getenv("SEMA_HOST_ALIAS")
	if alias == "" || rawURL == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return rawURL
	}
	if p := u.Port(); p != "" {
		u.Host = alias + ":" + p
	} else {
		u.Host = alias
	}
	return u.String()
}

// ApplyTimezone — часовой пояс Сёмы: время в интерфейсе, расписания задач и
// таймеры считаются в нём. Пусто — пояс машины (в контейнере это UTC/MSK).
func (e *Engine) ApplyTimezone(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("часовой пояс %q не найден: %v", name, err)
		return
	}
	time.Local = loc
	log.Printf("часовой пояс: %s", name)
}

// Timezone — какой пояс сейчас в ходу.
func (e *Engine) Timezone() string {
	if v := e.store.Setting("app.timezone", ""); v != "" {
		return v
	}
	return time.Local.String()
}

// SetTimezone — сменить пояс: настройка плюс применение на лету.
func (e *Engine) SetTimezone(name string) error {
	if _, err := time.LoadLocation(strings.TrimSpace(name)); err != nil {
		return fmt.Errorf("пояс %q не найден — примеры: Europe/Ulyanovsk, Europe/Moscow, UTC", name)
	}
	if err := e.store.SetSetting("app.timezone", strings.TrimSpace(name)); err != nil {
		return err
	}
	e.ApplyTimezone(name)
	return nil
}

// AdoptClientTimezone — первый запрос клиента приносит свой пояс: если
// человек ничего не выбирал, берём его и запоминаем.
func (e *Engine) AdoptClientTimezone(name string) {
	name = strings.TrimSpace(name)
	if name == "" || e.store.Setting("app.timezone", "") != "" {
		return
	}
	if _, err := time.LoadLocation(name); err != nil {
		return
	}
	_ = e.SetTimezone(name)
}

// filterModel — кем размечать это письмо. Раньше здесь стояла «активная»
// модель вообще: роль «Фильтр писем» в разметке не участвовала, а закрытость
// ящика не проверялась — отправитель, тема и выжимка каждого письма уходили
// туда, куда указывал `llm.active`. Теперь работает роль, а письмо из
// закрытого ящика или помеченное секретным достаётся только домашней модели.
// Домашней нет — письмо остаётся неразмеченным с честной причиной: наружу
// такое не отдаём даже ради разметки спама.
func (e *Engine) filterModel(m *store.Message) llm.Provider {
	name := e.Role(RoleFilter).Model
	if name == "" {
		name = e.ActiveProvider()
	}
	// класс берётся из таблицы классов, а не считается заново по теме и
	// выжимке: пометка человека «это секретное» лежит именно там, и путь,
	// который пересчитывал приметы сам, её не видел вовсе. Заодно
	// проверяется и чувствительное — прежде его здесь не смотрели совсем
	allow, _ := e.outsideCut(e.ModelIsCloud(name), e.ClassOfMessage(m), m.FromAddr)
	if !e.mailboxClosedForCloud(m.Account) && allow {
		return e.provider(name)
	}
	// письмо домашнее: облачной модели его не показываем
	if !e.ModelIsCloud(name) {
		return e.provider(name)
	}
	if home := e.homeModel(); home != "" {
		return e.provider(home)
	}
	m.Reason = reasonNoTrusted
	return nil
}

// TLSOn — есть ли у Сёмы защищённый порт. Без него браузер не даст ни
// подписаться на пуши, ни поставить значок на экран — и об этом надо
// сказать там, где человек включает канал, а не оставлять его гадать.
func (e *Engine) TLSOn() bool { return e.conf.TLS.Cert != "" && e.conf.TLS.Key != "" }
