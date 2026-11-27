package max

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/coder/websocket"
	"github.com/pierrec/lz4/v4"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	frameHeaderSize = 10
	maxFramePayload = 8 << 20
)

func init() {
	// Расширение MAX с номером 1 содержит целое, упакованное в MessagePack.
	msgpack.RegisterExtDecoder(1, json.Number(""), decodeWideInteger)
}

func decodeWideInteger(decoder *msgpack.Decoder, value reflect.Value, size int) error {
	if size < 1 || size > 9 {
		return fmt.Errorf("MAX: неверная длина целого")
	}
	data := make([]byte, size)
	if err := decoder.ReadFull(data); err != nil {
		return err
	}
	reader := bytes.NewReader(data)
	number, err := msgpack.NewDecoder(reader).DecodeInterface()
	if err != nil || reader.Len() != 0 {
		return fmt.Errorf("MAX: неверное целое")
	}
	switch number.(type) {
	case int8, int16, int32, int64, uint8, uint16, uint32, uint64:
		value.SetString(fmt.Sprint(number))
		return nil
	default:
		return fmt.Errorf("MAX: неподдерживаемое целое %T", number)
	}
}

func encodeFrame(cmd, seq, opcode int, payload any) ([]byte, error) {
	var out bytes.Buffer
	out.Write(make([]byte, frameHeaderSize))
	if payload != nil {
		encoder := msgpack.NewEncoder(&out)
		encoder.SetCustomStructTag("json")
		if err := encoder.Encode(payload); err != nil {
			return nil, fmt.Errorf("MAX: кодирование кадра: %w", err)
		}
	}
	data := out.Bytes()
	size := len(data) - frameHeaderSize
	if size > maxFramePayload {
		return nil, fmt.Errorf("MAX: слишком большой кадр")
	}
	data[0], data[1] = proto, byte(cmd)
	binary.BigEndian.PutUint16(data[2:4], uint16(seq))
	binary.BigEndian.PutUint16(data[4:6], uint16(opcode))
	data[7], data[8], data[9] = byte(size>>16), byte(size>>8), byte(size)
	return data, nil
}

func decodeFrame(kind websocket.MessageType, data []byte) (frame, error) {
	if kind != websocket.MessageBinary || len(data) < frameHeaderSize || data[0] != proto {
		return frame{}, fmt.Errorf("MAX: неверный заголовок кадра")
	}
	size := int(data[7])<<16 | int(data[8])<<8 | int(data[9])
	if size != len(data)-frameHeaderSize || size > maxFramePayload {
		return frame{}, fmt.Errorf("MAX: неверная длина кадра")
	}
	f := frame{
		Cmd:    int(data[1]),
		Seq:    int(int16(binary.BigEndian.Uint16(data[2:4]))),
		Opcode: int(binary.BigEndian.Uint16(data[4:6])),
	}
	if size == 0 {
		return f, nil
	}
	payload := data[frameHeaderSize:]
	if ratio := int(data[6]); ratio != 0 {
		decoded := make([]byte, min(maxFramePayload, size*ratio))
		n, err := lz4.UncompressBlock(payload, decoded)
		if err != nil || n == 0 {
			return frame{}, fmt.Errorf("MAX: неверный сжатый кадр")
		}
		payload = decoded[:n]
	}
	input := bytes.NewReader(payload)
	decoder := msgpack.NewDecoder(input)
	decoder.SetMapDecoder(decodePayloadMap)
	value, err := decoder.DecodeInterface()
	if err != nil || input.Len() != 0 {
		return frame{}, fmt.Errorf("MAX: неверный MessagePack в кадре")
	}
	f.Payload, err = json.Marshal(value)
	if err != nil {
		return frame{}, fmt.Errorf("MAX: неподдерживаемое значение в кадре: %w", err)
	}
	return f, nil
}

func decodePayloadMap(decoder *msgpack.Decoder) (any, error) {
	count, err := decoder.DecodeMapLen()
	if err != nil {
		return nil, err
	}
	values := make(map[string]any)
	for range count {
		key, err := decoder.DecodeInterface()
		if err != nil {
			return nil, err
		}
		switch key.(type) {
		case string, json.Number, int8, int16, int32, int64, uint8, uint16, uint32, uint64:
		default:
			return nil, fmt.Errorf("MAX: неподдерживаемый ключ %T", key)
		}
		value, err := decoder.DecodeInterface()
		if err != nil {
			return nil, err
		}
		values[fmt.Sprint(key)] = value
	}
	return values, nil
}
