package mail

// Сборка письма для отправки.
//
// До сегодняшнего дня Сёма не умела отправлять почту вовсе: SMTP в коде не
// было ни строки, а «Ответить» заканчивалось черновиком в чате. Отсюда
// главное решение: **по умолчанию Сёма кладёт ответ в «Черновики» ящика**,
// а не отправляет. Черновик отменяем, отправленное письмо — нет.
//
// Здесь только сборка: превратить «кому, тема, текст» в правильное письмо
// RFC 5322. Ни сети, ни ящика — поэтому это можно проверить целиком, а
// отправку и запись в «Черновики» пусть проверяет жизнь.
//
// Три вещи, ради которых это отдельный файл.
//
// Заголовки ответа. Ответ без `In-Reply-To` и `References` — это новое
// письмо с похожей темой: почтовый клиент собеседника покажет его отдельной
// веткой, и разговор развалится. Сёма только что научилась читать эти
// заголовки у входящих — и обязана ставить их у исходящих.
//
// Кодировка. Русская тема и русский текст в письме кодируются по-разному
// (RFC 2047 для темы, MIME для тела), и «плывущие» кракозябры у получателя
// — самый частый способ выглядеть сломанным.
//
// Длина строк. SMTP не обязан принимать строки длиннее 998 байт, а русский
// абзац такую длину набирает легко.

import (
	"fmt"
	"mime"
	"strings"
	"time"
)

// Message — то, что отправляем или кладём в черновики.
type Message struct {
	From       string // адрес ящика
	FromName   string
	To         []string
	Cc         []string
	Subject    string
	Text       string
	ReplyTo    *Fetched // на какое письмо отвечаем; nil — новое письмо
	InReplyTo  string   // Message-ID исходного, когда самого письма под рукой нет
	References []string // References исходного
	Date       time.Time
	ID         string // Message-ID нашего письма; пусто — соберём сами
}

// Build — письмо целиком, готовое к отправке или к APPEND в «Черновики».
func Build(msg Message) ([]byte, error) {
	from := strings.TrimSpace(msg.From)
	if from == "" {
		return nil, fmt.Errorf("не задан адрес отправителя")
	}
	if len(msg.To) == 0 {
		return nil, fmt.Errorf("не задан получатель")
	}
	date := msg.Date
	if date.IsZero() {
		date = time.Now()
	}
	id := msg.ID
	if id == "" {
		id = NewID(from)
	}

	var b strings.Builder
	header := func(name, value string) {
		if value == "" {
			return
		}
		b.WriteString(name + ": " + value + "\r\n")
	}
	header("Date", date.Format(time.RFC1123Z))
	header("Message-ID", "<"+strings.Trim(id, "<>")+">")
	header("From", addrWithName(msg.From, msg.FromName))
	header("To", strings.Join(msg.To, ", "))
	header("Cc", strings.Join(msg.Cc, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", replySubject(msg)))

	// цепочка: без неё ответ у собеседника встанет отдельной веткой
	if orig := origID(msg); orig != "" {
		header("In-Reply-To", "<"+orig+">")
		header("References", replyRefs(msg, orig))
	}
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	// подпись Сёмы обязательна: получатель вправе знать, что письмо
	// набрал ассистент, а не человек
	header("X-Mailer", "Сёма")
	b.WriteString("\r\n")
	b.WriteString(toQuotedPrintable(normalNewlines(msg.Text)))
	return []byte(b.String()), nil
}

// NewID — Message-ID нашего письма. Домен берём из адреса ящика: свой
// домен в чужом Message-ID выглядит подделкой и портит доставляемость.
func NewID(addr string) string {
	domain := "sema.local"
	if i := strings.LastIndex(addr, "@"); i >= 0 && i+1 < len(addr) {
		domain = addr[i+1:]
	}
	return fmt.Sprintf("sema-%d@%s", time.Now().UnixNano(), domain)
}

func addrWithName(addr, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return addr
	}
	return mime.QEncoding.Encode("utf-8", name) + " <" + addr + ">"
}

// replySubject — «Re: …» ставится один раз. «Re: Re: Re: Счёт» — верный
// признак письма, собранного механически.
func replySubject(msg Message) string {
	subject := strings.TrimSpace(msg.Subject)
	if origID(msg) == "" {
		return subject
	}
	if hasRePrefix(subject) {
		return subject
	}
	return "Re: " + subject
}

func hasRePrefix(subject string) bool {
	norm := strings.ToLower(strings.TrimSpace(subject))
	return strings.HasPrefix(norm, "re:") || strings.HasPrefix(norm, "re[") ||
		strings.HasPrefix(norm, "ответ:") || strings.HasPrefix(norm, "отв:")
}

func origID(msg Message) string {
	if msg.InReplyTo != "" {
		return strings.Trim(strings.TrimSpace(msg.InReplyTo), "<>")
	}
	if msg.ReplyTo != nil {
		return strings.Trim(strings.TrimSpace(msg.ReplyTo.MessageID), "<>")
	}
	return ""
}

// replyRefs — вся цепочка плюс исходное письмо последним, как велит
// RFC 5322. Повторы убираем: у длинных переписок References разрастается.
func replyRefs(msg Message, orig string) string {
	var refs []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.Trim(strings.TrimSpace(s), "<>")
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		refs = append(refs, "<"+s+">")
	}
	for _, r := range msg.References {
		add(r)
	}
	if msg.ReplyTo != nil {
		for _, r := range msg.ReplyTo.Refs {
			add(r)
		}
	}
	add(orig)
	return strings.Join(refs, " ")
}

// normalNewlines — в письме перевод строки это CRLF. Одинокий \n
// некоторые серверы считают концом данных.
func normalNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// toQuotedPrintable — кодирование тела. Пишем сами, а не берём
// `mime/quotedprintable`: тот пишет в io.Writer и не даёт управлять
// переносами так, как здесь нужно (мягкий перенос строго до 76 знаков,
// целые байты UTF-8 не разрываются).
func toQuotedPrintable(s string) string {
	var out strings.Builder
	length := 0
	emit := func(chunk string) {
		// мягкий перенос: строка не длиннее 76 знаков вместе с «=»
		if length+len(chunk) > 75 {
			out.WriteString("=\r\n")
			length = 0
		}
		out.WriteString(chunk)
		length += len(chunk)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\r' && i+1 < len(s) && s[i+1] == '\n':
			out.WriteString("\r\n")
			length = 0
			i++
		case c == '=':
			emit("=3D")
		case c >= 33 && c <= 126:
			emit(string(c))
		case c == ' ' || c == '\t':
			// пробел в конце строки обязан быть закодирован, иначе его
			// съест первый же сервер по дороге
			tail := i+1 >= len(s) || s[i+1] == '\r'
			if tail {
				emit(fmt.Sprintf("=%02X", c))
			} else {
				emit(string(c))
			}
		default:
			emit(fmt.Sprintf("=%02X", c))
		}
	}
	return out.String()
}
