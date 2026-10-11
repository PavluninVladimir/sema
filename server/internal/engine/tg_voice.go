package engine

// Голос в Telegram: вход.
//
// Голосовое сообщение (атрибут audio с флагом voice; музыка и прочие
// файлы с audio/ogg голосовыми не считаются) скачивается и распознаётся
// ролью «Распознавание речи». В админ-чате это вопрос хозяина — идёт Сёме
// так же, как набранный текст, а в переписке стоит «🎤 <текст>». В других
// чатах текст ложится в архив и ленту рядом с файлом, но облачной модели
// такое голосовое уходит только при `speech.outside` = all: чей это голос,
// неизвестно, и что в нём — тоже. Не вышло — в переписке строка «голосовое,
// не распознано: причина», без выдумок, и на такое Сёма не отвечает.
//
// Ответ хозяину идёт текстом, как на набранное: голосом Сёма пока не говорит.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/tg"
)

// voiceMark — чем в переписке начинается расшифровка голосового.
const voiceMark = "🎤 "

// heardVoice — голосовое, разобранное на входе.
type heardVoice struct {
	Text   string // расшифровка; пусто, если не вышло
	Err    error  // почему не вышло
	Name   string // имя файла, если он лёг в «Файлы»
	FileID int64
}

// line — как голосовое выглядит в переписке и в архиве. Файл, если он лёг в
// «Файлы», подписан в том же виде, что любое вложение: чип в пузыре его
// открывает.
func (v heardVoice) line(caption string) string {
	var b strings.Builder
	if caption = strings.TrimSpace(caption); caption != "" {
		b.WriteString(caption + " ")
	}
	if v.Err != nil {
		b.WriteString(voiceMark + "голосовое, не распознано: " + reasonLine(v.Err))
	} else {
		b.WriteString(voiceMark + v.Text)
	}
	if v.FileID > 0 {
		b.WriteString(fmt.Sprintf(" «%s» (#%d)", v.Name, v.FileID))
	}
	return strings.TrimSpace(b.String())
}

// reasonLine — причина одной строкой: в переписке она стоит рядом со
// словами, и многострочный ответ провайдера там ни к чему.
func reasonLine(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexAny(s, "\r\n"); i > 0 {
		s = s[:i]
	}
	return clip(s, 200)
}

// fetchTG — забрать вложение сообщения.
func (e *Engine) fetchTG(ctx context.Context, c *store.TGChat, f *tg.File) ([]byte, error) {
	if e.tgFetch != nil {
		return e.tgFetch(ctx, c, f)
	}
	cl, _, err := e.tgClientOf(c)
	if err != nil {
		return nil, err
	}
	return cl.Download(ctx, f.Location, int64(e.TGFileLimitMB())<<20)
}

// tgHearVoice — скачать и распознать входящее голосовое. Файл ложится в
// «Файлы» по праву чата «Файлы»: с уже готовой расшифровкой, чтобы при
// приёме файла её не платили второй раз.
func (e *Engine) tgHearVoice(ctx context.Context, c *store.TGChat, u tg.Update) heardVoice {
	v := heardVoice{Name: u.File.Name}
	raw, err := e.fetchTG(ctx, c, u.File)
	if err != nil {
		v.Err = fmt.Errorf("запись не скачалась: %w", err)
		return v
	}
	if len(raw) == 0 {
		v.Err = errors.New("пустая запись")
		return v
	}
	mime := u.File.MIME
	if mime == "" {
		mime = "audio/ogg"
	}
	// свой голос — только в админ-чате: это хозяин говорит с Сёмой. Чужие
	// чаты — чужие голоса, и облачной модели они не уходят без разрешения
	text, model, err := e.Hear(ctx, VoiceIn{Audio: raw, MIME: mime, Own: c.Admin && !u.Out, Consumer: "core:telegram"})
	if err != nil {
		v.Err = err
	} else {
		v.Text = text
	}
	if c.Files {
		var known *knownText
		if v.Err == nil {
			known = &knownText{Text: text, Via: "распознавание речи · " + model}
		}
		att, _, ferr := e.addFile(ctx, u.File.Name, mime, raw,
			FileSource{Kind: store.SourceTG, Ref: fmt.Sprintf("%s/%d/%d", c.Account, c.ID, u.MsgID), Account: c.Title}, nil, known)
		if ferr != nil {
			log.Printf("telegram: голосовое из «%s» не легло в файлы: %v", c.Title, ferr)
		} else if att != nil {
			v.FileID = att.ID
		}
	}
	return v
}

// tgHistoryVoice — как подписано голосовое в истории. Историю не
// расшифровываем: это деньги за каждую старую запись, о которой никто не
// просил. Живое голосовое разбирает onTGMessage.
func tgHistoryVoice(f *tg.File) string {
	if f.Duration > 0 {
		return fmt.Sprintf("[голосовое, %d с]", f.Duration)
	}
	return "[голосовое]"
}

// tgVoiceUnheard — голосовое хозяина не распознано: в переписке Сёмы остаётся
// его строка, а хозяину в Telegram уходит короткое «не разобрал» с причиной.
// Вопроса нет, и Сёма на него не отвечает: сочинить содержание записи он не
// вправе, а молчание после «прочитано» хозяин принял бы за поломку.
func (e *Engine) tgVoiceUnheard(ctx context.Context, c *store.TGChat, semaChat int64, line string, reason error) {
	thread := e.chatForOutput(semaChat)
	_ = e.store.AddChatMsg(thread, "user", line)
	msg := "Не разобрал голосовое: " + reasonLine(reason)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	var err error
	if talk := e.tgTalkWith(c, true); talk != nil {
		var id int
		if id, err = talk.tr.SendText(sctx, talk.peer, msg); err == nil {
			talk.archive(id, msg)
		}
	} else {
		err = e.TGSendApproved(sctx, c.Account, c.ID, c.Topic, msg)
	}
	if err != nil {
		log.Printf("telegram: хозяину не сказали, что голосовое не разобрано: %v", err)
		return
	}
	_ = e.store.AddChatMsg(semaChat, "sent", msg) // видно, что ушло хозяину
}
