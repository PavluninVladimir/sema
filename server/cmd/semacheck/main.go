// semacheck — диагностика Сёмы на живых данных. Читает ту же базу, тот же
// ключ шифрования и те же настройки ящика, что и сервер, поэтому показывает
// правду, а не гипотезу.
//
//	semacheck folders               папки ящика: счётчики, непрочитанные, что разбираем
//	semacheck locate <message-id>   в какой папке письмо лежит сейчас
//	semacheck parts  <message-id>   части письма и вложения так, как их видит Сёма
//	semacheck rag    <вопрос>       ровно тот контекст, что уйдёт модели
//	semacheck module <имя>          выполнить модуль с его правами
//	semacheck perf                  замер экранов работающей Сёмы (токен в SEMA_TOKEN), см. perf.go
package main

import (
	"context"
	"fmt"
	"github.com/emersion/go-imap/v2"
	"io"
	"log"
	"os"
	"strings"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
	"github.com/PavluninVladimir/sema/server/internal/google"
	"github.com/PavluninVladimir/sema/server/internal/mail"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("что проверить: folders · locate <message-id> · parts <message-id> · rag <вопрос> · module <имя> · key · perf")
		os.Exit(2)
	}
	// проверка ключа идёт до базы: она нужна как раз тогда, когда база в
	// контейнере, а на руках только шифрованное значение из неё
	if os.Args[1] == "key" {
		checkKey()
		return
	}
	// замер ходит к работающей Сёме по HTTP: ни базы, ни ключа ему не надо
	if os.Args[1] == "perf" {
		perfMain(os.Args[2:])
		return
	}
	conf, st := open()
	defer st.Close()
	arg := strings.Join(os.Args[2:], " ")

	switch os.Args[1] {
	case "folders":
		folders(dial(conf, st))
	case "locate":
		sess := dial(conf, st)
		defer sess.Close()
		box, uid, ok := sess.Locate(arg)
		fmt.Printf("письмо %s: папка=%q uid=%d найдено=%v\n", arg, box, uid, ok)
	case "module-parse":
		// module-parse <файл> — как Сёма разберёт ответ модели с блоком sema-module
		data, err := os.ReadFile(arg)
		if err != nil {
			log.Fatal(err)
		}
		m, err := engine.ParseModuleFromAnswer(string(data))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("name=%s title=%s needs=%v tools=%v tasks=%d screens=%d code=%d байт\n%s\n",
			m.Name, m.Title, m.Needs, m.Tools, len(m.Tasks), len(m.Screens), len(m.Code), m.Code)
	case "move":
		// move '<message-id> <папка>' — вернуть письмо в папку (например, отправленное в «Отправленные»)
		id, box, _ := strings.Cut(arg, " ")
		sess := dial(conf, st)
		defer sess.Close()
		from, uid, ok := sess.Locate(id)
		if !ok {
			log.Fatalf("письмо %s не найдено", id)
		}
		if err := sess.MoveUIDs(from, []imap.UID{imap.UID(uid)}, box); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("письмо %s: %s → %s\n", id, from, box)
	case "probe":
		// probe '<папка>' '<message-id>' — как сервер ищет письмо
		box, id, _ := strings.Cut(arg, " ")
		sess := dial(conf, st)
		defer sess.Close()
		fmt.Print(sess.ProbeMessageID(box, id))
	case "parts":
		parts(dial(conf, st), arg)
	case "rag":
		// SEMA_MODEL=имя — контекст глазами этой модели (облачная не видит закрытые ящики)
		eng := newEngine(conf, st)
		model := os.Getenv("SEMA_MODEL")
		if model == "" {
			model = eng.ActiveProvider()
		}
		fmt.Println(eng.RAGContextFor(context.Background(), arg, model))
	case "module":
		res, err := newEngine(conf, st).RunModule(context.Background(), arg, "")
		if err != nil {
			log.Fatal(err)
		}
		if res.Err != nil {
			fmt.Println("ошибка модуля:", res.ErrText, "строка", res.ErrLine)
			return
		}
		fmt.Println("модуль отработал за", res.Duration, "· вернул:", res.Value)
	default:
		log.Fatalf("не знаю проверки %q", os.Args[1])
	}
}

// open — база и ключ шифрования, как у сервера.
func open() (*config.Config, *store.Store) {
	conf, err := config.Load("config.yaml")
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(conf.DB)
	if err != nil {
		log.Fatal(err)
	}
	if box, err := secret.New(os.Getenv(conf.KeyEnvName())); err == nil && box != nil {
		st.UseSecretBox(box)
	}
	return conf, st
}

func newEngine(conf *config.Config, st *store.Store) *engine.Engine {
	eng, err := engine.New(conf, st)
	if err != nil {
		log.Fatal(err)
	}
	return eng
}

// dial входит в основной ящик тем же способом, что и сервер.
func dial(conf *config.Config, st *store.Store) *mail.Session {
	// SEMA_ACCOUNT=имя — другой ящик, не основной из файла запуска
	if name := os.Getenv("SEMA_ACCOUNT"); name != "" {
		sess, err := newEngine(conf, st).DialMailbox(name)
		if err != nil {
			log.Fatal(err)
		}
		return sess
	}
	var tokens mail.TokenSource
	if conf.Mail.Auth == "gmail" {
		oauth := &google.OAuth{
			ClientID: conf.Mail.Google.ClientID, ClientSecret: conf.Mail.Google.Secret(),
			Store: st.Secrets(),
		}
		tokens = oauth.AccessToken
	}
	sess, err := mail.Dial(conf.Mail, tokens)
	if err != nil {
		log.Fatal(err)
	}
	return sess
}

func folders(sess *mail.Session) {
	defer sess.Close()
	archive, err := sess.ArchiveFolder()
	fmt.Println("архив:", archive, err)
	boxes, _ := sess.Mailboxes()
	src, _ := sess.SourceMailboxes(archive)
	inSrc := map[string]bool{}
	for _, b := range src {
		inSrc[b] = true
	}
	for _, b := range boxes {
		n, err := sess.CountIn(b.Name)
		if err != nil {
			continue
		}
		mark := "  пропускаем"
		if inSrc[b.Name] {
			mark = "→ разбираем"
		}
		fmt.Printf("%-34s %5d писем, %4d непрочитанных  %s\n", b.Name, n, sess.UnseenIn(b.Name), mark)
	}
}

func parts(sess *mail.Session, messageID string) {
	defer sess.Close()
	box, uid, ok := sess.Locate(messageID)
	if !ok {
		log.Fatal("письмо не найдено")
	}
	fmt.Printf("папка %q, uid %d\n", box, uid)
	list, err := sess.PartsIn(box, uid)
	if err != nil {
		log.Fatal(err)
	}
	for _, p := range list {
		fmt.Println("  ", p)
	}
	atts, err := sess.FetchAttachmentsIn(box, uid, 50<<20)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("вложений по правилам Сёмы: %d\n", len(atts))
	for _, a := range atts {
		fmt.Printf("  - %s · %s · %d КБ\n", a.Filename, a.MIME, len(a.Data)/1024)
	}
}

// checkKey — подходит ли SEMA_KEY к тому, что лежит в базе. Шифрованное
// значение подаётся на вход: база может быть в контейнере, а вопрос
// «тот ли ключ» возникает раньше, чем до неё доберёшься. Само значение
// наружу не печатается — только «да» или «нет».
func checkKey() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	sealed := strings.TrimSpace(string(raw))
	if sealed == "" {
		log.Fatal("на вход нужно подать шифрованное значение из базы")
	}
	if !secret.Encrypted(sealed) {
		fmt.Println("это значение не зашифровано — проверять нечего")
		return
	}
	key := os.Getenv("SEMA_KEY")
	if strings.TrimSpace(key) == "" {
		log.Fatal("ключа нет: задайте SEMA_KEY")
	}
	box, err := secret.New(key)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := box.Open(sealed); err != nil {
		fmt.Println("ключ не подходит")
		os.Exit(1)
	}
	fmt.Println("ключ подходит")
}
