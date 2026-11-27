package wechat

import (
	"bytes"
	"testing"
)

func TestLonglinkStreaming(t *testing.T) {
	a, _ := packLonglink(24, 0, []byte{0, 0, 0, 6})
	b, _ := packLonglink(1000000238, 23, bytes.Repeat([]byte{2}, 256))
	both := append(bytes.Clone(a), b...)
	for cut := 0; cut < len(a); cut++ {
		d := longlinkDecoder{maxPayload: maxMessageBytes}
		if e := d.feed(both[:cut]); e != nil {
			t.Fatal(e)
		}
		p, e := d.next()
		if e != nil || p != nil {
			t.Fatalf("premature %d %v", cut, e)
		}
		if e = d.feed(both[cut:]); e != nil {
			t.Fatal(e)
		}
		p, e = d.next()
		if e != nil || p == nil || p.command != 24 {
			t.Fatalf("first %d %v", cut, e)
		}
		p, e = d.next()
		if e != nil || p == nil || p.sequence != 23 || len(p.payload) != 256 {
			t.Fatalf("second %d %v", cut, e)
		}
	}
}
func TestLonglinkRejectsInvalid(t *testing.T) {
	d := longlinkDecoder{maxPayload: 100}
	d.feed([]byte{0, 0, 0, 15})
	if _, e := d.next(); e == nil {
		t.Fatal("short length accepted")
	}
	d = longlinkDecoder{maxPayload: 100}
	d.feed([]byte{0, 0, 0, 16, 0, 0, 0, 1})
	if _, e := d.next(); e == nil {
		t.Fatal("bad envelope accepted")
	}
	if _, e := packLonglink(1, 1, make([]byte, 65504)); e == nil {
		t.Fatal("oversized packet accepted")
	}
}
