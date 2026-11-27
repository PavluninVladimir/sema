package wechat

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

func fileSendRequest(state *accountState, destination, clientID string, sentAt uint32, file uploadedFile) ([]byte, error) {
	var content bytes.Buffer
	content.WriteString("<appmsg appid=\"\" sdkver=\"0\">")
	tag := func(name, value string) {
		content.WriteString("<" + name + ">")
		xml.EscapeText(&content, []byte(value))
		content.WriteString("</" + name + ">")
	}
	tag("title", file.name)
	tag("des", "")
	tag("action", "")
	tag("type", "6")
	tag("showtype", "0")
	tag("soundtype", "0")
	for _, name := range []string{"mediatagname", "messageext", "messageaction", "content", "url", "lowurl", "dataurl", "lowdataurl"} {
		tag(name, "")
	}
	tag("contentattr", "0")
	content.WriteString("<appattach>")
	tag("totallen", strconv.Itoa(file.size))
	tag("attachid", "")
	tag("emoticonmd5", "")
	tag("fileext", strings.TrimPrefix(path.Ext(file.name), "."))
	tag("cdnattachurl", file.id)
	tag("aeskey", hex.EncodeToString(file.key))
	tag("encryver", "1")
	content.WriteString("</appattach>")
	for _, name := range []string{"extinfo", "sourceusername", "sourcedisplayname", "thumburl"} {
		tag(name, "")
	}
	tag("md5", file.md5)
	tag("statextstr", "")
	content.WriteString("</appmsg>")
	tag("fromusername", state.Username)

	app, _ := appendBytes(nil, 1, []byte(state.Username))
	app, _ = appendBytes(app, 2, nil)
	app, _ = appendUint(app, 3, 0)
	app, _ = appendBytes(app, 4, []byte(destination))
	app, _ = appendUint(app, 5, 6)
	app, _ = appendBytes(app, 6, content.Bytes())
	app, _ = appendUint(app, 7, uint64(sentAt))
	app, _ = appendBytes(app, 8, []byte(clientID))
	app, _ = appendUint(app, 10, 1)
	for _, n := range []uint32{13, 14, 15} {
		app, _ = appendBytes(app, n, nil)
	}
	device := loginDevice{id: state.DeviceID, kind: state.DeviceKind, version: state.ClientVersion}
	base, err := baseRequest(device, state.UIN, 0)
	if err != nil {
		return nil, err
	}
	out, _ := appendBytes(nil, 1, base)
	out, _ = appendBytes(out, 2, app)
	out, _ = appendBytes(out, 5, []byte(file.md5))
	out, _ = appendUint(out, 9, 0)
	out, _ = appendUint(out, 10, 0)
	return appendUint(out, 11, 2)
}

func decodeFileSend(input []byte, clientID string) (Delivery, error) {
	f, err := decodeFields(input)
	if err != nil {
		return Delivery{}, err
	}
	code, err := baseCode(f)
	if err != nil {
		return Delivery{}, err
	}
	if code != 0 {
		return Delivery{Rejected: true, Code: code}, nil
	}
	for _, n := range []uint32{5, 7} {
		if _, err = readUint32(f, n); err != nil {
			return Delivery{}, err
		}
	}
	id, err := scalarField(f, 9)
	if err != nil || id == 0 {
		return Delivery{}, errors.New("WeChat не подтвердил идентификатор отправленного файла")
	}
	matched := false
	for _, n := range []uint32{2, 3, 4, 6, 10, 11, 13} {
		field, err := optionalBytes(f, n)
		if err != nil {
			return Delivery{}, err
		}
		matched = matched || bytes.Equal(field, []byte(clientID))
	}
	if !matched {
		return Delivery{}, errors.New("ответ WeChat не сопоставился с отправкой файла")
	}
	return Delivery{ServerID: strconv.FormatUint(id, 10)}, nil
}

// SendFile uploads an ordinary file and submits the resulting attachment once.
func SendFile(ctx context.Context, snapshot []byte, destination, name string, body []byte, clientID uint32, begin func() error) (Delivery, error) {
	if len(body) == 0 || len(body) > maxCDNFile-17 {
		return Delivery{}, errors.New("файл WeChat должен помещаться в 25 МиБ")
	}
	state, err := readSecretSnapshot(snapshot)
	if err != nil {
		return Delivery{}, err
	}
	defer state.close()
	session, err := dialMMTLS(ctx, state.LongHost)
	if err != nil {
		return Delivery{}, err
	}
	defer session.close()
	if err = begin(); err != nil {
		return Delivery{}, err
	}
	conf, err := session.cdnBootstrap(ctx, state)
	if err != nil {
		return Delivery{}, err
	}
	defer clear(conf.authKey)
	taskID := state.Username + "_" + strconv.FormatUint(uint64(clientID), 10)
	uploaded, err := uploadCDNFile(conf, state, destination, name, taskID, body, func(f cdnFields) (cdnFields, error) {
		return conf.post(ctx, "/uploadv3", f)
	})
	if err != nil {
		return Delivery{}, err
	}
	defer clear(uploaded.key)
	request, err := fileSendRequest(state, destination, taskID, uint32(time.Now().Unix()), uploaded)
	if err != nil {
		return Delivery{}, err
	}
	defer clear(request)
	packet, err := prepareAuthenticated(state, 222, request)
	if err != nil {
		return Delivery{}, err
	}
	raw, _, err := session.authenticatedRPC(ctx, state.ShortHost, packet, "/cgi-bin/micromsg-bin/sendappmsg")
	if err != nil {
		var rejected *serviceError
		if errors.As(err, &rejected) {
			return Delivery{Rejected: true, Code: rejected.code}, nil
		}
		return Delivery{}, fmt.Errorf("%w: %v", errDeliveryUnknown, err)
	}
	defer clear(raw)
	return decodeFileSend(raw, taskID)
}
