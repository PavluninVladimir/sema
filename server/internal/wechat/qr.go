package wechat

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"image/png"
	"time"
	"unicode/utf8"
)

type loginDevice struct {
	id      []byte
	kind    string
	version int32
}

type loginQR struct {
	image, identifier, notifyKey []byte
	width, height                uint32
}

type loginPoll struct {
	status                   *uint32
	credential3, credential4 []byte
	lanChallenge             bool
}

func baseRequest(device loginDevice, owner, task uint32) ([]byte, error) {
	if len(device.id) == 0 || len(device.id) > 1024 || device.kind == "" || len(device.kind) > 1024 ||
		!utf8.ValidString(device.kind) || device.version == 0 {
		return nil, errors.New("неверный профиль устройства WeChat")
	}
	b, _ := appendBytes(nil, 1, []byte{0})
	b, _ = appendUint(b, 2, uint64(owner))
	b, _ = appendBytes(b, 3, device.id)
	b, _ = appendUint(b, 4, uint64(int64(device.version)))
	b, _ = appendBytes(b, 5, []byte(device.kind))
	return appendUint(b, 6, uint64(task))
}

func loginRequestPrefix(device loginDevice) ([]byte, error) {
	base, err := baseRequest(device, 0, 0)
	if err != nil {
		return nil, err
	}
	buffer, err := loginNonceBuffer()
	if err != nil {
		return nil, err
	}
	request, err := appendBytes(nil, 1, base)
	if err != nil {
		return nil, err
	}
	return appendBytes(request, 2, buffer)
}

func makeQRRequest(device loginDevice, computerName string) ([]byte, error) {
	if len(computerName) == 0 || len(computerName) > 255 || !utf8.ValidString(computerName) {
		return nil, errors.New("неверное имя устройства WeChat")
	}
	b, err := loginRequestPrefix(device)
	if err != nil {
		return nil, err
	}
	b, _ = appendUint(b, 3, 0)
	return appendBytes(b, 4, []byte(computerName))
}

func makePollRequest(device loginDevice, identifier []byte, now time.Time) ([]byte, error) {
	if len(identifier) == 0 || len(identifier) > 4096 || now.UnixMilli() < 0 {
		return nil, errors.New("неверный идентификатор или время входа WeChat")
	}
	b, err := loginRequestPrefix(device)
	if err != nil {
		return nil, err
	}
	b, _ = appendBytes(b, 3, identifier)
	b, _ = appendUint(b, 4, uint64(uint32(now.UnixMilli())/1000))
	return appendUint(b, 5, 0)
}

type serviceError struct {
	operation string
	code      int32
}

func (e *serviceError) Error() string {
	return fmt.Sprintf("WeChat: %s, код %d", e.operation, e.code)
}

func loginResponseFields(input []byte, operation string) ([]wireField, error) {
	f, err := decodeFields(input)
	if err != nil {
		return nil, err
	}
	code, err := baseCode(f)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &serviceError{operation: operation, code: code}
	}
	return f, nil
}

func decodeLoginQR(input []byte) (loginQR, error) {
	var qr loginQR
	f, err := loginResponseFields(input, "получение QR")
	if err != nil {
		return qr, err
	}
	if _, err = scalarField(f, 4); err != nil {
		return qr, err
	}
	raw, err := dataField(f, 2)
	if err != nil {
		return qr, err
	}
	image, err := unpackBuffer(raw)
	if err != nil {
		return qr, err
	}
	identifier, err := dataField(f, 3)
	if err != nil {
		return qr, err
	}
	raw, err = dataField(f, 5)
	if err != nil {
		return qr, err
	}
	key, err := unpackBuffer(raw)
	if err != nil {
		return qr, err
	}
	if len(identifier) == 0 || len(identifier) > 4096 || len(key) == 0 || len(key) > 1024 ||
		len(image) < 33 || !bytes.Equal(image[:8], []byte("\x89PNG\r\n\x1a\n")) ||
		binary.BigEndian.Uint32(image[8:12]) != 13 || string(image[12:16]) != "IHDR" {
		return qr, errors.New("неверное состояние QR WeChat")
	}
	width, height := binary.BigEndian.Uint32(image[16:20]), binary.BigEndian.Uint32(image[20:24])
	if width < 21 || width > 4096 || height < 21 || height > 4096 {
		return qr, errors.New("неверные размеры QR WeChat")
	}
	if _, err := png.Decode(bytes.NewReader(image)); err != nil {
		return qr, errors.New("повреждено изображение QR WeChat")
	}
	return loginQR{image: image, identifier: identifier, notifyKey: key, width: width, height: height}, nil
}

func optionalBytes(fields []wireField, number uint32) ([]byte, error) {
	f, err := optionalField(fields, number)
	if err != nil || f == nil {
		return nil, err
	}
	if f.kind != 2 {
		return nil, fmt.Errorf("неверный тип поля %d", number)
	}
	return f.data, nil
}

func decodeLoginPoll(input, key []byte) (loginPoll, error) {
	var out loginPoll
	f, err := loginResponseFields(input, "проверка QR")
	if err != nil {
		return out, err
	}
	packet, err := optionalBytes(f, 3)
	if err != nil || packet == nil {
		return out, err
	}
	wrap, err := decodeFields(packet)
	if err != nil {
		return out, err
	}
	if _, err = scalarField(wrap, 2); err != nil {
		return out, err
	}
	data, err := dataField(wrap, 1)
	if err != nil {
		return out, err
	}
	encrypted, err := unpackBuffer(data)
	if err != nil {
		return out, err
	}
	plain, err := legacyCBC(encrypted, key, true)
	if err != nil {
		return out, err
	}
	defer clear(plain)
	notify, err := decodeFields(plain)
	if err != nil {
		return out, err
	}
	risk, err := optionalBytes(notify, 17)
	if err != nil {
		return out, err
	}
	if risk != nil {
		if len(risk) > 4096 {
			return out, errors.New("слишком большое расширение входа")
		}
		data, err := unpackBuffer(risk)
		if err != nil {
			return out, err
		}
		extension, err := decodeFields(data)
		if err != nil {
			return out, err
		}
		state, err := optionalField(extension, 1)
		if err != nil {
			return out, err
		}
		if state != nil {
			if state.kind != 0 {
				return out, errors.New("неверный тип состояния входа")
			}
			out.lanChallenge = state.scalar == 11
		}
	}
	status, err := scalarField(notify, 2)
	if err != nil {
		return loginPoll{}, err
	}
	if status > uint64(^uint32(0)) {
		return loginPoll{}, errors.New("неверный статус входа")
	}
	status32 := uint32(status)
	out.status = &status32
	out.credential3, err = optionalBytes(notify, 3)
	if err != nil {
		return loginPoll{}, err
	}
	out.credential4, err = optionalBytes(notify, 4)
	if err != nil {
		return loginPoll{}, err
	}
	if status == 2 && !out.lanChallenge && (len(out.credential3) == 0 || len(out.credential3) > 65536 || len(out.credential4) == 0 || len(out.credential4) > 65536) {
		return loginPoll{}, errors.New("подтверждение входа без ключей")
	}
	return out, nil
}

func loginNonceBuffer() ([]byte, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return packBuffer(nonce[:])
}
