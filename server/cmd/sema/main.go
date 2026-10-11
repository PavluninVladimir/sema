package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/PavluninVladimir/sema/server/internal/auth"
	"github.com/PavluninVladimir/sema/server/internal/version"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/api"
	"github.com/PavluninVladimir/sema/server/internal/backup"
	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/google"
	"github.com/PavluninVladimir/sema/server/internal/host"
	"github.com/PavluninVladimir/sema/server/internal/journal"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func main() {
	confPath := flag.String("config", "config.yaml", "файл запуска: адрес, каталог данных, имя переменной с ключом")
	listen := flag.String("listen", "", "адрес прослушивания (перекрывает файл и окружение)")
	data := flag.String("data", "", "каталог данных: база, вложения, служебные файлы")
	googleLogin := flag.Bool("google-login", false, "интерактивный вход в Gmail (OAuth), сохранить токен и выйти")
	classifyScan := flag.Bool("classify-scan", false, "замер классификации: прогнать новые приметы по сохранённым промптам и напечатать числа; база читается без замка, содержимое не печатается")
	doctor := flag.Bool("doctor", false, "самоосмотр: каталог, замок, база, ключ, порт, инструменты — работает и когда Сёма не поднимается")
	resetAuth := flag.Bool("reset-auth", false, "запасной вход: удалить все passkey и сессии, после чего первый ключ заводится заново — с этой машины или кодом первой установки")
	backupTo := flag.String("backup", "", "снять копию в файл .tgz: база, файлы, опись с хэшами и счётчиками; ключ не кладётся; можно на работающей Сёме")
	noFiles := flag.Bool("no-files", false, "копия без файлов вложений (только база и опись)")
	restore := flag.String("restore", "", "восстановить из копии после самоосмотра (хэши, integrity_check, миграции, расшифровка, счётчики); Сёма должна быть остановлена")
	rekey := flag.Bool("rekey", false, "сменить ключ шифрования: нужна свежая копия (-fresh-backup), Сёма остановлена; печатает новую фразу восстановления в stdout — в контейнере без --rm она останется в docker logs")
	freshBackup := flag.String("fresh-backup", "", "путь к копии не старше часа — обязательна для -rekey")
	yadiskPut := flag.String("yadisk-put", "", "выгрузить файл на Яндекс.Диск родным API (не WebDAV): тело идёт на узел загрузки, печатается его адрес и скорость")
	yadiskDir := flag.String("yadisk-dir", "Сёма/копии", "папка на Диске для -yadisk-put")
	yadiskToken := flag.String("yadisk-token", "YaDiskToken", "имя секрета с OAuth-токеном Диска")
	yadiskRm := flag.String("yadisk-rm", "", "убрать файл из папки копий на Яндекс.Диске (по имени)")
	yadiskList := flag.Bool("yadisk-list", false, "что лежит в папке копий на Яндекс.Диске (спрашиваем сам Диск, а не узел загрузки)")
	flag.Parse()

	conf, err := config.Load(*confPath)
	if err != nil {
		log.Fatalf("файл запуска %s: %v", *confPath, err)
	}
	if *listen != "" {
		conf.Listen = *listen
	}
	if *data != "" {
		conf.SetDataDir(*data, "аргумент -data")
	}
	if *doctor {
		os.Exit(inspect(conf))
	}
	if *classifyScan {
		os.Exit(measureClasses(conf))
	}
	if *yadiskRm != "" {
		if err := yaDiskRemove(conf, *yadiskDir, *yadiskRm, *yadiskToken); err != nil {
			log.Fatalf("уборка на Диске: %v", err)
		}
		return
	}
	if *yadiskList {
		if err := yaDiskList(conf, *yadiskDir, *yadiskToken); err != nil {
			log.Fatalf("список на Диске: %v", err)
		}
		return
	}
	if *yadiskPut != "" {
		if err := yaDiskPut(conf, *yadiskPut, *yadiskDir, *yadiskToken); err != nil {
			log.Fatalf("выгрузка родным API: %v", err)
		}
		return
	}
	if *backupTo != "" {
		if err := backup.Make(conf, *backupTo, *noFiles); err != nil {
			log.Fatalf("копия: %v", err)
		}
		return
	}
	if *rekey {
		if _, err := backup.RotateKey(conf, *freshBackup); err != nil {
			log.Fatalf("смена ключа: %v", err)
		}
		return
	}
	if *restore != "" {
		if err := backup.Restore(conf, *restore); err != nil {
			log.Fatalf("восстановление: %v", err)
		}
		return
	}
	if err := os.MkdirAll(conf.Data, 0o755); err != nil {
		log.Fatalf("каталог данных %s: нет прав на запись (%v)", conf.Data, err)
	}
	// журнал заводится сразу после каталога и до всего остального: если
	// сломается что-то дальше, причина должна остаться на диске, а не
	// только на экране, который закроют вместе с окном
	jrn, jerr := journal.Open(conf.Data, conf.Log.LimitMB, conf.Log.Keep, os.Stderr)
	if jerr != nil {
		log.Printf("журнал не завёлся (%v) — пишу только на экран", jerr)
	} else {
		// сам ключ шифрования прячем поимённо: приметы ловят вид значения,
		// а ключ выглядит как обычная строка
		jrn.Hide(os.Getenv(conf.KeyEnvName()))
		log.SetOutput(jrn)
		log.SetFlags(log.Ldate | log.Ltime)
		defer jrn.Close()
	}
	// первой строкой — откуда взялся каталог данных. Это самый частый
	// вопрос при разборе «а где мои письма», и он должен остаться в файле,
	// а не только на экране, который закроют вместе с окном
	log.Printf("Сёма · сборка %s", version.String())
	log.Printf("каталог данных: %s (%s)", conf.Data, conf.DataFrom)

	// замок берётся до открытия базы: два сервера на одном каталоге — это
	// повреждённая база, а не «второй сервер». Держится дескриптором всё
	// время жизни процесса, убитый процесс отпускает его сам
	lock, err := host.Acquire(conf.Data)
	if err != nil {
		log.Fatal(err)
	}
	defer lock.Release()

	st, err := store.Open(conf.DB)
	if err != nil {
		log.Fatalf("база %s: %v", conf.DB, err)
	}
	if *resetAuth {
		// запасной путь, когда все ключи потеряны: только с самой машины
		if err := st.ResetAuth(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("passkey и сессии удалены: заведите ключ заново на /login — с этой машины или кодом первой установки, который Сёма напечатает при следующем старте")
		return
	}
	defer st.Close()

	if *googleLogin {
		if conf.Mail.Auth != "gmail" {
			log.Fatal("для -google-login укажите в конфиге mail.auth: gmail и mail.google.client_id")
		}
		oauth := &google.OAuth{
			ClientID:     conf.Mail.Google.ClientID,
			ClientSecret: conf.Mail.Google.Secret(),
			Store:        st,
		}
		if err := oauth.Login(context.Background()); err != nil {
			log.Fatalf("google login: %v", err)
		}
		log.Println("Готово: refresh-токен сохранён, запускайте сервер обычным способом.")
		return
	}

	eng, err := engine.New(conf, st)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go eng.Run(ctx)
	// приём агентов: только если человек задал адрес на экране «Агенты»
	// Telegram присылает сообщения сам: держим соединение, а не опрашиваем
	go eng.ListenTelegram(ctx)
	go eng.ListenMAX(ctx)
	go func() {
		if err := eng.ServeAgents(ctx); err != nil {
			log.Printf("агенты: слушать не вышло: %v", err)
		}
	}()
	// шлюз: здесь звонит Сёма, а не он. Адреса нет — ничего не
	// происходит, и дом работает как прежде: обязательным звеном шлюз
	// не становится
	go eng.DialGateway(ctx)

	// прошлый запуск закрылся начисто или его убили — знать об этом надо
	// до первой работы: незавершённые внешние действия разбираются иначе
	if !st.CleanShutdown() {
		log.Printf("прошлый запуск закрылся неначисто (убит или упал) — проверяю целостность базы")
		// на аккуратный выход не рассчитываем: проверяем сами и говорим
		// вслух, а не надеемся, что всё обошлось
		if res, err := st.QuickCheck(); err != nil {
			log.Printf("проверка целостности не прошла: %v", err)
		} else if res != "ok" {
			log.Printf("ВНИМАНИЕ: база жалуется — %s", res)
		} else {
			log.Printf("целостность базы в порядке")
		}
	}
	st.MarkRunning()

	// защищённый порт — рядом с обычным, если дали сертификат. Телефону
	// без него нельзя ни войти по passkey, ни поставить значок на экран
	if conf.TLS.Cert != "" && conf.TLS.Key != "" {
		go func() {
			if err := api.ServeTLS(ctx, conf.TLS.Listen, conf.TLS.Cert, conf.TLS.Key, eng); err != nil {
				log.Printf("защищённый порт не поднялся: %v", err)
			}
		}()
	}

	// адрес входа задан руками и не сходится с прослушиванием — говорим при
	// старте, но не переписываем: молчаливая правка настройки безопасности
	// хуже предупреждения
	if w := auth.OriginWarning(st.Setting(auth.SettingOrigin, ""), conf.Listen, tlsListenOrEmpty(conf)); w != "" {
		log.Printf("ВНИМАНИЕ: %s", w)
	}
	// значение по умолчанию у проверки владельца ключа сменилось — об этом
	// говорим при старте: человек узнаёт о сужении доступа из журнала, а не
	// из того, что вход вдруг перестал пускать
	if n := auth.UserVerificationNotice(st.Setting(auth.SettingUserVerification, "")); n != "" {
		log.Printf("вход: %s", n)
	}
	// защищённый порт по умолчанию слушает только петлю. В контейнере петля
	// своя, и снаружи такого порта не видно вовсе — говорим об этом при старте,
	// а не оставляем человека гадать, почему https молчит
	if l := tlsListenOrEmpty(conf); l != "" && host.InContainer() && loopbackOnly(l) {
		log.Printf("ВНИМАНИЕ: защищённый порт слушает %s — внутри контейнера это только его собственная петля, "+
			"и снаружи порт не откроется; задайте SEMA_TLS_LISTEN=0.0.0.0:8443, а наружу публикуйте порт на нужном адресе хозяина", l)
	}
	// код первой установки: пока ключей нет, Сёма печатает одноразовый код с
	// адресом входа — в Docker браузер хозяина приходит не по петле, и без кода
	// первый ключ было бы не завести. Строка выдачи — единственная, где журнал
	// пропускает код; дальше он прячется везде (auth/setup.go)
	if code, until, ok := auth.IssueSetupCode(st); ok {
		a := auth.New(st)
		a.SetListen(conf.Listen)
		line := auth.SetupLine(code, until, a.Origin())
		if jrn != nil {
			jrn.Issue(os.Stdout, line, code)
			jrn.Hide(auth.SetupCodeForms(code)...)
		} else {
			fmt.Println(line)
		}
	}
	if err := api.Serve(ctx, conf.Listen, eng); err != nil {
		if api.PortBusy(err) {
			log.Fatal(api.PortDiagnostic(conf.Listen, "listen", *confPath, host.InContainer()))
		}
		log.Fatal(err)
	}
	// сюда доходим по сигналу: пометка ставится последней транзакцией перед
	// закрытием базы, иначе «закрыт чисто» соврёт при падении на выходе
	// журнал предзаписи сводим со своим сроком: не успели — закрываемся
	// всё равно, он дочитается при следующем открытии
	if err := st.CheckpointLog(10 * time.Second); err != nil {
		log.Printf("%v", err)
	}
	st.MarkCleanShutdown()
	log.Println("закрылся начисто")
}

// loopbackOnly — адрес прослушивания только на петле: «127.0.0.1:8443»,
// «[::1]:8443», «localhost:8443».
func loopbackOnly(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// tlsListenOrEmpty — адрес защищённого порта, если он настроен.
func tlsListenOrEmpty(conf *config.Config) string {
	if conf.TLS.Cert != "" && conf.TLS.Key != "" {
		return conf.TLS.Listen
	}
	return ""
}
