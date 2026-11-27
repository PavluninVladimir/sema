package wechat

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type uploadedFile struct {
	name, id, md5 string
	size          int
	key           []byte
}

func encodedCDNFName(name string) string {
	const digits = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-_.!~*'()", rune(c)) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(digits[c>>4])
			b.WriteByte(digits[c&15])
		}
	}
	return b.String()
}

func uploadCDNFile(conf cdnConfig, state *accountState, destination, name, fileKey string, plain []byte, post func(cdnFields) (cdnFields, error)) (_ uploadedFile, err error) {
	return uploadCDN(conf, state, destination, name, fileKey, plain, 5, nil, post)
}

func uploadCDN(conf cdnConfig, state *accountState, destination, name, fileKey string, plain []byte, kind uint32,
	variants func(cdnFields, []byte) error, post func(cdnFields) (cdnFields, error)) (_ uploadedFile, err error) {
	var out uploadedFile
	if len(plain) == 0 || len(plain) > maxCDNFile-17 || len(name) == 0 || len(name) > 255 || name == "." || name == ".." ||
		!utf8.ValidString(name) || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) ||
		len(destination) == 0 || len(destination) > 512 || !utf8.ValidString(destination) ||
		strings.ContainsFunc(destination, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) || fileKey == "" || len(fileKey) > 1024 {
		return out, errors.New("неверные параметры файла WeChat")
	}
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return out, err
	}
	defer func() {
		if err != nil {
			clear(key)
		}
	}()
	encrypted, err := cdnCrypt(plain, key, false)
	if err != nil {
		return out, err
	}
	defer clear(encrypted)
	recip, err := legacyCBC([]byte(destination), []byte("wxusrname2016cdn"), false)
	if err != nil {
		return out, err
	}
	receiver := "@cdn2_" + hex.EncodeToString(recip)
	digest := md5.Sum(plain)
	whole := hex.EncodeToString(digest[:])
	var remoteID string
	const size = 65536
	for start, seq := 0, 1; start < len(encrypted); start, seq = start+size, seq+1 {
		end := min(start+size, len(encrypted)) - 1
		piece := encrypted[start : end+1]
		fields := cdnShared(conf, state, seq, kind)
		for k, v := range map[string]int{"hasthumb": 0, "compresstype": 0, "nocheckaeskey": 1, "enablehit": 0,
			"existancecheck": 0, "apptype": 0, "totalsize": len(encrypted), "rawtotalsize": len(plain),
			"thumbtotalsize": 0, "rawthumbsize": 0, "encthumbcrc": 0, "advideoflag": 0, "rangestart": start, "rangeend": end} {
			fields[k] = strconv.AppendInt(nil, int64(v), 10)
		}
		sum := md5.Sum(piece)
		digest := hex.EncodeToString(sum[:])
		for k, v := range map[string]string{"touser": receiver, "filekey": fileKey, "localname": encodedCDNFName(name),
			"filedatamd5": digest, "filemd5": whole, "rawfilemd5": whole, "blockmd5": digest, "rawthumbmd5": ""} {
			fields[k] = []byte(v)
		}
		fields["filedata"] = piece
		if start == 0 && variants != nil {
			if err := variants(fields, key); err != nil {
				return out, err
			}
		}
		reply, err := post(fields)
		if err != nil {
			return out, err
		}
		code, err := cdnNumber(reply, "retcode")
		if err != nil {
			return out, err
		}
		if code != 0 {
			return out, errors.New("CDN WeChat отклонил загрузку")
		}
		for k, want := range map[string]int{"seq": seq, "rangestart": start, "rangeend": end} {
			if _, exists := reply[k]; exists {
				got, err := cdnNumber(reply, k)
				if err != nil || got != want {
					return out, errors.New("ответ CDN не соответствует запросу WeChat")
				}
			}
		}
		if raw, exists := reply["fileid"]; exists {
			value := string(raw)
			if !validUploadedID(value) || (remoteID != "" && remoteID != value) {
				return out, errors.New("неверный идентификатор файла CDN WeChat")
			}
			remoteID = value
		}
	}
	if remoteID == "" {
		return out, errors.New("CDN WeChat не выдал идентификатор файла")
	}
	return uploadedFile{name: name, id: remoteID, md5: whole, size: len(plain), key: key}, nil
}

func validUploadedID(value string) bool {
	if len(value) == 0 || len(value) > 4096 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.ContainsRune("-_@.+/=", c)) {
			return false
		}
	}
	return true
}
