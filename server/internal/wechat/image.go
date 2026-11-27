package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"hash/adler32"
	"image"
	"image/jpeg"
	"strconv"
)

type imagePreview struct {
	jpeg          []byte
	width, height int
}

func resizeJPEG(src image.Image, edge, quality int) (imagePreview, error) {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if size := max(w, h); size > edge {
		w, h = max(1, w*edge/size), max(1, h*edge/size)
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			scaled.Set(x, y, src.At(bounds.Min.X+x*bounds.Dx()/w, bounds.Min.Y+y*bounds.Dy()/h))
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, scaled, &jpeg.Options{Quality: quality}); err != nil {
		return imagePreview{}, err
	}
	return imagePreview{b.Bytes(), w, h}, nil
}

func adlerZero(data []byte) uint32 {
	const prime = 65521
	sum := adler32.Checksum(data)
	return (((sum>>16 + prime - uint32(len(data)%prime)) % prime) << 16) | ((sum&0xffff + prime - 1) % prime)
}

func imageCDNFields(f cdnFields, key []byte, width, height int, standard, thumb imagePreview) error {
	mid, err := cdnCrypt(standard.jpeg, key, false)
	if err != nil {
		return err
	}
	small, err := cdnCrypt(thumb.jpeg, key, false)
	if err != nil {
		return err
	}
	for name, n := range map[string]int{
		"imgheight": height, "imgwidth": width, "midimgrawsize": len(standard.jpeg), "midimgtotalsize": len(mid),
		"midimgchecksum": int(adlerZero(mid)), "file_format": 0, "mid_format": 0,
		"hasthumb": 1, "thumbfiletype": 3, "thumbtotalsize": len(small), "rawthumbsize": len(thumb.jpeg),
		"thumbcrc": int(adlerZero(thumb.jpeg)), "encthumbcrc": int(adlerZero(small)),
	} {
		f[name] = strconv.AppendUint(nil, uint64(uint32(n)), 10)
	}
	md5Mid, md5Thumb := md5.Sum(standard.jpeg), md5.Sum(thumb.jpeg)
	f["midimgmd5"] = []byte(hex.EncodeToString(md5Mid[:]))
	f["rawthumbmd5"] = []byte(hex.EncodeToString(md5Thumb[:]))
	f["midimgdata"] = mid
	f["thumbdata"] = small
	return nil
}

func imageSendRequest(state *accountState, peer, client string, original uploadedFile, standard, thumbnail imagePreview) ([]byte, error) {
	base, err := baseRequest(loginDevice{id: state.DeviceID, kind: state.DeviceKind, version: state.ClientVersion}, state.UIN, 0)
	if err != nil {
		return nil, err
	}
	out, _ := appendBytes(nil, 1, base)
	for i, text := range []string{client, state.Username, peer} {
		wrapped, _ := appendBytes(nil, 1, []byte(text))
		out, _ = appendBytes(out, uint32(i+2), wrapped)
	}
	thumbnailSize := uint64(len(thumbnail.jpeg))
	out, _ = appendUint(out, 5, thumbnailSize)
	out, _ = appendUint(out, 6, 0)
	out, _ = appendUint(out, 7, thumbnailSize)
	buffer, _ := packBuffer(nil)
	out, _ = appendBytes(out, 8, buffer)
	for _, field := range []struct {
		number uint32
		value  uint64
	}{{9, 3}, {11, 1}, {12, 2}, {13, 0}} {
		out, _ = appendUint(out, field.number, field.value)
	}
	key := []byte(hex.EncodeToString(original.key))
	defer clear(key)
	out, _ = appendBytes(out, 15, []byte(original.id))
	out, _ = appendBytes(out, 16, []byte(original.id))
	out, _ = appendBytes(out, 17, key)
	out, _ = appendUint(out, 18, 1)
	out, _ = appendUint(out, 19, uint64(original.size))
	out, _ = appendUint(out, 20, uint64(len(standard.jpeg)))
	out, _ = appendBytes(out, 21, []byte(original.id))
	out, _ = appendUint(out, 22, thumbnailSize)
	out, _ = appendUint(out, 23, uint64(thumbnail.height))
	out, _ = appendUint(out, 24, uint64(thumbnail.width))
	out, _ = appendBytes(out, 25, key)
	out, _ = appendBytes(out, 27, []byte(original.md5))
	for _, n := range []uint32{28, 30, 36, 41} {
		out, _ = appendUint(out, n, 0)
	}
	return out, nil
}

func decodeImageSend(raw []byte, client, sender, peer string) (Delivery, error) {
	f, err := decodeFields(raw)
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
	for _, n := range []uint32{2, 6, 7, 8, 9} {
		if _, err := readUint32(f, n); err != nil {
			return Delivery{}, err
		}
	}
	for i, expected := range []string{client, sender, peer} {
		raw, err := dataField(f, uint32(i+3))
		if err != nil {
			return Delivery{}, err
		}
		wrapped, err := decodeFields(raw)
		if err != nil {
			return Delivery{}, err
		}
		v, err := dataField(wrapped, 1)
		if err != nil || string(v) != expected {
			return Delivery{}, errors.New("ответ WeChat не сопоставился с отправкой изображения")
		}
	}
	serverID, err := scalarField(f, 10)
	if err != nil || serverID == 0 {
		return Delivery{}, errors.New("WeChat не подтвердил идентификатор изображения")
	}
	return Delivery{ServerID: strconv.FormatUint(serverID, 10)}, nil
}

// SendJPEG sends the original JPEG with resized standard and thumbnail copies.
func SendJPEG(ctx context.Context, snapshot []byte, peer, name string, original []byte, clientID uint32, begin func() error) (Delivery, error) {
	if len(original) == 0 || len(original) > maxCDNFile-17 {
		return Delivery{}, errors.New("изображение WeChat должно помещаться в 25 МиБ")
	}
	size, err := jpeg.DecodeConfig(bytes.NewReader(original))
	if err != nil || size.Width <= 0 || size.Height <= 0 || size.Width > 4096 || size.Height > 4096 {
		return Delivery{}, errors.New("нужно изображение JPEG размером до 4096 × 4096")
	}
	img, err := jpeg.Decode(bytes.NewReader(original))
	if err != nil {
		return Delivery{}, err
	}
	standard, err := resizeJPEG(img, 1280, 82)
	if err != nil {
		return Delivery{}, err
	}
	defer clear(standard.jpeg)
	thumb, err := resizeJPEG(img, 320, 75)
	if err != nil {
		return Delivery{}, err
	}
	defer clear(thumb.jpeg)
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
	conf, err := session.cdnBootstrap(ctx, state)
	if err != nil {
		return Delivery{}, err
	}
	defer clear(conf.authKey)
	task := state.Username + "_" + strconv.FormatUint(uint64(clientID), 10)
	upload, err := uploadCDN(conf, state, peer, name, task, original, 1, func(f cdnFields, key []byte) error {
		return imageCDNFields(f, key, size.Width, size.Height, standard, thumb)
	}, func(f cdnFields) (cdnFields, error) {
		return conf.post(ctx, "/uploadv3", f)
	})
	if err != nil {
		return Delivery{}, err
	}
	defer clear(upload.key)
	request, err := imageSendRequest(state, peer, task, upload, standard, thumb)
	if err != nil {
		return Delivery{}, err
	}
	defer clear(request)
	packet, err := prepareAuthenticated(state, 110, request)
	if err != nil {
		return Delivery{}, err
	}
	if err := begin(); err != nil {
		return Delivery{}, err
	}
	raw, _, err := session.authenticatedRPC(ctx, state.ShortHost, packet, "/cgi-bin/micromsg-bin/uploadmsgimg")
	if err != nil {
		var rejected *serviceError
		if errors.As(err, &rejected) {
			return Delivery{Rejected: true, Code: rejected.code}, nil
		}
		return Delivery{}, err
	}
	defer clear(raw)
	return decodeImageSend(raw, task, state.Username, peer)
}
