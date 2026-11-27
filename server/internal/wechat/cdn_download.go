package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

type cdnDescriptor struct {
	id       string
	key      []byte
	fileType uint32
	size     int
	digest   string
}

func cdnNumber(fields cdnFields, k string) (int, error) {
	v := fields[k]
	if len(v) == 0 || len(v) > 16 {
		return 0, fmt.Errorf("неверное поле CDN WeChat %s", k)
	}
	return strconv.Atoi(string(v))
}

func cdnShared(conf cdnConfig, state *accountState, sequence int, kind uint32) cdnFields {
	f := cdnFields{"clientostype": []byte(state.DeviceKind), "authkey": conf.authKey}
	for k, v := range map[string]uint64{
		"ver": uint64(conf.version), "weixinnum": uint64(conf.uin), "seq": uint64(sequence),
		"clientversion": uint64(uint32(state.ClientVersion)), "nettype": 1, "acceptdupack": 1,
		"safeproto": 1, "filetype": uint64(kind), "wxchattype": 0, "lastretcode": 0,
		"ipseq": 0, "largesvideo": 0, "sourceflag": 0,
	} {
		f[k] = strconv.AppendUint(nil, v, 10)
	}
	return f
}

func (conf cdnConfig) post(ctx context.Context, path string, fields cdnFields) (cdnFields, error) {
	if (path != "/download" && path != "/uploadv3") || len(conf.servers) == 0 || !publicCDNAddress(conf.servers[0]) {
		return nil, errors.New("недопустимый маршрут CDN WeChat")
	}
	body, err := packCDN(fields)
	if err != nil {
		return nil, err
	}
	defer clear(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+conf.servers[0].String()+":443"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.GetBody = nil
	req.Close = true
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "MicroMessenger Client")
	req.Header.Set("C-Seq", string(fields["seq"]))
	resp, err := shortHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CDN WeChat: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCDNBody+1))
	if err != nil {
		return nil, err
	}
	return unpackCDN(raw)
}

func downloadCDN(conf cdnConfig, state *accountState, desc cdnDescriptor, post func(cdnFields) (cdnFields, error)) ([]byte, error) {
	if desc.id == "" || len(desc.id) > 8192 || strings.ContainsFunc(desc.id, unicode.IsControl) ||
		len(desc.key) != 16 || desc.size <= 0 || desc.size > maxCDNFile ||
		(desc.fileType != 1 && desc.fileType != 2 && desc.fileType != 3 && desc.fileType != 5) {
		return nil, errors.New("неверное описание файла WeChat")
	}
	digest, err := hex.DecodeString(desc.digest)
	if err != nil || len(digest) != md5.Size {
		return nil, errors.New("нет контрольной суммы файла WeChat")
	}
	const chunk = 65536
	total := (desc.size/16 + 1) * 16
	encrypted := make([]byte, 0, total)
	for seq := 1; len(encrypted) < total && seq <= (total+chunk-1)/chunk; seq++ {
		offset := len(encrypted)
		end := min(offset+chunk, total) - 1
		fields := cdnShared(conf, state, seq, desc.fileType)
		for k, v := range map[string]int{"wxautostart": 0, "downpicformat": 1, "rangestart": offset, "rangeend": end} {
			fields[k] = strconv.AppendInt(nil, int64(v), 10)
		}
		fields["wxmsgflag"] = nil
		fields["fileid"] = []byte(desc.id)
		reply, err := post(fields)
		if err != nil {
			return nil, err
		}
		for k, want := range map[string]int{"retcode": 0, "rangestart": offset, "rangeend": end, "totalsize": total} {
			got, err := cdnNumber(reply, k)
			if err != nil || got != want {
				return nil, fmt.Errorf("не совпадает поле %s в ответе CDN WeChat", k)
			}
		}
		if _, ok := reply["seq"]; ok {
			got, err := cdnNumber(reply, "seq")
			if err != nil || got != seq {
				return nil, errors.New("номер ответа CDN WeChat не совпадает")
			}
		}
		data := reply["filedata"]
		if len(data) != end-offset+1 {
			return nil, errors.New("обрезанный фрагмент файла WeChat")
		}
		encrypted = append(encrypted, data...)
	}
	if len(encrypted) != total {
		return nil, errors.New("файл WeChat получен не полностью")
	}
	plain, err := cdnCrypt(encrypted, desc.key, true)
	if err != nil {
		return nil, err
	}
	checksum := md5.Sum(plain)
	if len(plain) != desc.size || subtle.ConstantTimeCompare(checksum[:], digest) != 1 {
		clear(plain)
		return nil, errors.New("размер или контрольная сумма файла WeChat не совпадает")
	}
	return plain, nil
}

func (s *mmtlsSession) cdnBootstrap(ctx context.Context, state *accountState) (cdnConfig, error) {
	device := loginDevice{id: state.DeviceID, kind: state.DeviceKind, version: state.ClientVersion}
	base, err := baseRequest(device, state.UIN, 0)
	if err != nil {
		return cdnConfig{}, err
	}
	request, err := appendBytes(nil, 1, base)
	if err != nil {
		return cdnConfig{}, err
	}
	packet, err := prepareAuthenticated(state, 379, request)
	if err != nil {
		return cdnConfig{}, err
	}
	raw, _, err := s.authenticatedRPC(ctx, state.ShortHost, packet, "/cgi-bin/micromsg-bin/getcdndns")
	if err != nil {
		return cdnConfig{}, err
	}
	defer clear(raw)
	return decodeCDNConfig(raw, state.UIN)
}
