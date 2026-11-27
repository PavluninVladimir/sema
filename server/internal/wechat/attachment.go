package wechat

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

type FileInfo struct {
	Name string
	Size int
}

type fileCarrier struct {
	Image struct {
		Original string `xml:"cdnbigimgurl,attr"`
		Size     int    `xml:"hdlength,attr"`
		Key      string `xml:"aeskey,attr"`
		Digest   string `xml:"md5,attr"`
	} `xml:"img"`
	App struct {
		Title      string `xml:"title"`
		Type       int    `xml:"type"`
		Digest     string `xml:"md5"`
		Attachment struct {
			Size   int    `xml:"totallen"`
			FileID string `xml:"cdnattachurl"`
			Key    string `xml:"aeskey"`
		} `xml:"appattach"`
	} `xml:"appmsg"`
}

// decodeFileCarrier recognizes native small app files. Group messages may
// prefix their XML with a sender line, which is not part of the descriptor.
func decodeFileCarrier(body []byte) (FileInfo, cdnDescriptor, error) {
	invalid := errors.New("неподдерживаемое вложение WeChat")
	if len(body) > 256*1024 || !utf8.Valid(body) {
		return FileInfo{}, cdnDescriptor{}, invalid
	}
	if split := bytes.Index(body, []byte(":\n")); split >= 0 && split < 512 && !bytes.Contains(body[:split], []byte("<")) {
		body = body[split+2:]
	}
	body = bytes.TrimSpace(body)
	if bytes.HasPrefix(body, []byte("<?xml")) {
		end := bytes.Index(body, []byte("?>"))
		if end < 0 {
			return FileInfo{}, cdnDescriptor{}, invalid
		}
		body = bytes.TrimSpace(body[end+2:])
	}
	var m fileCarrier
	if bytes.HasPrefix(body, []byte("<msg>")) || bytes.HasPrefix(body, []byte("<msg ")) {
		if err := xml.Unmarshal(body, &m); err != nil {
			return FileInfo{}, cdnDescriptor{}, invalid
		}
	} else {
		wrapper := append([]byte("<msg>"), body...)
		wrapper = append(wrapper, []byte("</msg>")...)
		if err := xml.Unmarshal(wrapper, &m); err != nil {
			return FileInfo{}, cdnDescriptor{}, invalid
		}
	}
	if m.Image.Original != "" {
		f := m.Image
		digest, err := hex.DecodeString(f.Digest)
		if err != nil || len(digest) != 16 || f.Size <= 0 || f.Size > maxCDNFile || !validUploadedID(f.Original) {
			return FileInfo{}, cdnDescriptor{}, invalid
		}
		key, err := hex.DecodeString(f.Key)
		if err != nil || len(key) != 16 {
			clear(key)
			return FileInfo{}, cdnDescriptor{}, invalid
		}
		return FileInfo{Name: "Изображение.jpg", Size: f.Size}, cdnDescriptor{id: f.Original, key: key, fileType: 1, size: f.Size, digest: f.Digest}, nil
	}
	f := m.App.Attachment
	key, err := hex.DecodeString(f.Key)
	if err != nil || len(key) != 16 || m.App.Type != 6 || f.Size <= 0 || f.Size > maxCDNFile || f.FileID == "" {
		return FileInfo{}, cdnDescriptor{}, invalid
	}
	md5, err := hex.DecodeString(m.App.Digest)
	if err != nil || len(md5) != 16 {
		clear(key)
		return FileInfo{}, cdnDescriptor{}, invalid
	}
	name := strings.TrimSpace(m.App.Title)
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) {
		clear(key)
		return FileInfo{}, cdnDescriptor{}, invalid
	}
	return FileInfo{Name: name, Size: f.Size}, cdnDescriptor{id: f.FileID, key: key, fileType: 5, size: f.Size, digest: m.App.Digest}, nil
}

func InspectFile(body []byte) (FileInfo, error) {
	info, descriptor, err := decodeFileCarrier(body)
	clear(descriptor.key)
	return info, err
}

// DownloadFile resolves a descriptor contained in an already-authorized
// message. No destination URL or key comes from the UI.
func DownloadFile(ctx context.Context, snapshot, body []byte) (FileInfo, []byte, error) {
	info, descriptor, err := decodeFileCarrier(body)
	if err != nil {
		return FileInfo{}, nil, err
	}
	defer clear(descriptor.key)
	state, err := readSecretSnapshot(snapshot)
	if err != nil {
		return FileInfo{}, nil, err
	}
	defer state.close()
	session, err := dialMMTLS(ctx, state.LongHost)
	if err != nil {
		return FileInfo{}, nil, err
	}
	defer session.close()
	conf, err := session.cdnBootstrap(ctx, state)
	if err != nil {
		return FileInfo{}, nil, err
	}
	defer clear(conf.authKey)
	data, err := downloadCDN(conf, state, descriptor, func(fields cdnFields) (cdnFields, error) {
		return conf.post(ctx, "/download", fields)
	})
	if err != nil {
		return FileInfo{}, nil, err
	}
	if descriptor.fileType == 1 && !bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}) {
		clear(data)
		return FileInfo{}, nil, errors.New("оригинал изображения WeChat не является JPEG")
	}
	return info, data, nil
}
