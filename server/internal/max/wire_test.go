package max

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/pierrec/lz4/v4"
)

func TestFrameRoundTrip(t *testing.T) {
	want := "9223372036854775807"
	body := map[string]any{"chatId": int64(9223372036854775807), "text": strings.Repeat("сообщение ", 50)}
	data, err := encodeFrame(cmdReply, 123, opChats, body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data[:7], []byte{10, 1, 0, 123, 0, 53, 0}) {
		t.Fatalf("неверный заголовок: %v", data[:7])
	}
	payload := data[frameHeaderSize:]
	compressed := make([]byte, lz4.CompressBlockBound(len(payload)))
	var compressor lz4.Compressor
	size, err := compressor.CompressBlock(payload, compressed)
	if err != nil || size == 0 {
		t.Fatalf("не удалось сжать тестовый кадр: %d, %v", size, err)
	}
	packed := append(bytes.Clone(data[:frameHeaderSize]), compressed[:size]...)
	packed[6] = byte((len(payload) + size - 1) / size)
	packed[7], packed[8], packed[9] = byte(size>>16), byte(size>>8), byte(size)

	for _, packet := range [][]byte{data, packed} {
		f, err := decodeFrame(websocket.MessageBinary, packet)
		if err != nil {
			t.Fatal(err)
		}
		var received struct {
			ChatID json.Number
			Text   string
		}
		if err := json.Unmarshal(f.Payload, &received); err != nil {
			t.Fatal(err)
		}
		if f.Cmd != cmdReply || f.Seq != 123 || f.Opcode != opChats || received.ChatID.String() != want || received.Text != body["text"] {
			t.Fatalf("другой кадр: %+v %+v", f, received)
		}
	}
	for _, packet := range [][]byte{data[:2], data[:len(data)-1], append(bytes.Clone(data), 0)} {
		if _, err := decodeFrame(websocket.MessageBinary, packet); err == nil {
			t.Fatal("принят неверный кадр")
		}
	}
}

func TestFrameWithWideInteger(t *testing.T) {
	// MessagePack fixext 8 не подходит для девяти байт числа с префиксом.
	data := []byte{10, 1, 0, 1, 0, 53, 0, 0, 0, 12,
		0xc7, 9, 1, 0xcf, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	f, err := decodeFrame(websocket.MessageBinary, data)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(f.Payload); got != "18446744073709551615" {
		t.Fatalf("потеряна точность целого: %s", got)
	}
}
