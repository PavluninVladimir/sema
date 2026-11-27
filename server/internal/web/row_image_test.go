package web

import (
	"bytes"
	"encoding/base64"
	"html"
	"image"
	"image/png"
	"regexp"
	"strings"
	"testing"
)

func TestInlineQRCodeRendersDecodablePNG(t *testing.T) {
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewGray(image.Rect(0, 0, 185, 185))); err != nil {
		t.Fatal(err)
	}
	rendered, err := Render(Screen{Addr: "settings:wechat", Blocks: []Block{{
		Kind: "строка", Open: true, InlinePNG: imageBytes.Bytes(),
		ImageURL: "/api/wechat/qr?t=123",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	markup := string(rendered)
	match := regexp.MustCompile(`src="data:image/png;base64,([^"]+)"`).FindStringSubmatch(markup)
	if len(match) != 2 || strings.Contains(markup, `src="/api/wechat/qr`) {
		t.Fatal("QR must render without a separate image request")
	}
	encoded := html.UnescapeString(match[1])
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	picture, err := png.Decode(bytes.NewReader(data))
	if err != nil || picture.Bounds().Dx() != 185 {
		t.Fatalf("invalid embedded PNG: %v", err)
	}
}

func TestExpandedRowRendersImageWithoutOtherDetails(t *testing.T) {
	rendered, err := Render(Screen{Addr: "settings:wechat", Blocks: []Block{{
		Kind: "строка", Title: "QR для входа в WeChat", Open: true,
		ImageURL: "/api/wechat/qr?t=123",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	for _, want := range []string{`class="expansion-wrap" open`, `src="/api/wechat/qr?t=123"`, `alt="QR для входа в WeChat"`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s in rendered QR row", want)
		}
	}
}
