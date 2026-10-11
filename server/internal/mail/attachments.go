package mail

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime/quotedprintable"
	"strings"

	"github.com/emersion/go-imap/v2"
)

type Attachment struct {
	Filename string
	MIME     string
	Data     []byte
}

// FetchAttachments скачивает вложения письма из INBOX (по частям BODYSTRUCTURE).
// maxSize — предел на одно вложение в байтах, крупнее — пропускаем.
func (s *Session) FetchAttachments(uid uint32, maxSize int64) ([]*Attachment, error) {
	return s.FetchAttachmentsIn("INBOX", uid, maxSize)
}

// FetchAttachmentsIn — вложения письма из произвольной папки (спам тоже).
func (s *Session) FetchAttachmentsIn(mailbox string, uid uint32, maxSize int64) ([]*Attachment, error) {
	if _, err := s.c.Select(mailbox, nil).Wait(); err != nil {
		return nil, err
	}
	uidSet := imap.UIDSetNum(imap.UID(uid))
	msgs, err := s.c.Fetch(uidSet, &imap.FetchOptions{
		UID: true,
		// Extended обязателен: без него сервер не присылает Content-Disposition,
		// и вложения, у которых имя указано только там (а не в name= внутри
		// Content-Type), выглядят как части тела и молча теряются
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch bodystructure: %w", err)
	}
	if len(msgs) == 0 || msgs[0].BodyStructure == nil {
		return nil, nil
	}

	type partRef struct {
		path     []int
		filename string
		mime     string
		encoding string
		size     int64
	}
	var parts []partRef
	msgs[0].BodyStructure.Walk(func(path []int, part imap.BodyStructure) bool {
		sp, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		name := sp.Filename()
		if name == "" {
			return true // не вложение (тело письма и т.п.)
		}
		parts = append(parts, partRef{
			path:     append([]int(nil), path...),
			filename: name,
			mime:     strings.ToLower(sp.Type + "/" + sp.Subtype),
			encoding: strings.ToLower(sp.Encoding),
			size:     int64(sp.Size),
		})
		return true
	})

	var out []*Attachment
	for _, p := range parts {
		if p.size > maxSize {
			continue
		}
		section := &imap.FetchItemBodySection{Part: p.path, Peek: true}
		fm, err := s.c.Fetch(uidSet, &imap.FetchOptions{
			UID:         true,
			BodySection: []*imap.FetchItemBodySection{section},
		}).Collect()
		if err != nil {
			return out, fmt.Errorf("fetch part %v: %w", p.path, err)
		}
		if len(fm) == 0 {
			continue
		}
		raw := fm[0].FindBodySection(section)
		data, err := decodeBody(raw, p.encoding)
		if err != nil || len(data) == 0 {
			continue
		}
		out = append(out, &Attachment{Filename: p.filename, MIME: p.mime, Data: data})
	}
	return out, nil
}

func decodeBody(raw []byte, encoding string) ([]byte, error) {
	switch encoding {
	case "base64":
		clean := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(raw))
		return base64.StdEncoding.DecodeString(clean)
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(strings.NewReader(string(raw))))
	default:
		return raw, nil
	}
}
