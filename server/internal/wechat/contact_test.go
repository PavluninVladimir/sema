package wechat

import "testing"

func TestDecodeContact(t *testing.T) {
	// SKBuiltinString field 1 wraps each text value in ModContact.
	peer := []byte{0x0a, 0x06, 0x0a, 0x04, 'p', 'e', 'e', 'r'}
	nickname := []byte{0x12, 0x06, 0x0a, 0x04, 'N', 'a', 'm', 'e'}
	remark := []byte{0x52, 0x06, 0x0a, 0x04, 'H', 'e', 'r', 'e'}
	for _, tc := range []struct {
		input []byte
		want  string
	}{
		{append(append([]byte{}, peer...), nickname...), "Name"},
		{append(append([]byte{}, peer...), remark...), "Here"},
	} {
		got, err := decodeContact(tc.input)
		if err != nil || got != (Contact{Peer: "peer", Title: tc.want}) {
			t.Fatalf("decoded contact %+v: %v", got, err)
		}
	}
	if _, err := decodeContact(nickname); err == nil {
		t.Fatal("contact with missing peer accepted")
	}
}
