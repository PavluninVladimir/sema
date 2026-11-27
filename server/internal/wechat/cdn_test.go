package wechat

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"net/netip"
	"strconv"
	"testing"
)

func TestCDNFramingAndAddresses(t *testing.T) {
	fields := cdnFields{"retcode": []byte("0"), "data": []byte{1, 2, 3}}
	wire, err := packCDN(fields)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unpackCDN(wire)
	if err != nil || !bytes.Equal(got["data"], fields["data"]) {
		t.Fatalf("frame: %v %v", got, err)
	}
	for _, bad := range [][]byte{wire[:len(wire)-1], append(bytes.Clone(wire), wire...)} {
		if _, err := unpackCDN(bad); err == nil {
			t.Fatal("invalid CDN frame accepted")
		}
	}
	for _, ip := range []string{"127.0.0.1", "192.168.1.1", "100.64.0.1", "169.254.169.254", "::1", "::ffff:8.8.8.8"} {
		if publicCDNAddress(netip.MustParseAddr(ip)) {
			t.Errorf("non-public CDN address accepted: %s", ip)
		}
	}
}

func TestCDNEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 16)
	for _, n := range []int{0, 1, 16, 8193} {
		plain := bytes.Repeat([]byte{3}, n)
		encrypted, err := cdnCrypt(plain, key, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := cdnCrypt(encrypted, key, true)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("round trip size %d: %v", n, err)
		}
		if _, err = cdnCrypt(encrypted[:len(encrypted)-1], key, true); err == nil {
			t.Fatal("accepted truncated ciphertext")
		}
	}
}

func TestDownloadCDNVerifiesChunksAndDigest(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 16)
	data := bytes.Repeat([]byte("text"), 20000)
	encrypted, err := cdnCrypt(data, key, false)
	if err != nil {
		t.Fatal(err)
	}
	digest := md5.Sum(data)
	desc := cdnDescriptor{id: "sample", key: key, fileType: 5, size: len(data), digest: hex.EncodeToString(digest[:])}
	post := func(req cdnFields) (cdnFields, error) {
		start, err := cdnNumber(req, "rangestart")
		if err != nil {
			return nil, err
		}
		end, err := cdnNumber(req, "rangeend")
		if err != nil {
			return nil, err
		}
		return cdnFields{"retcode": []byte("0"), "totalsize": []byte(strconv.Itoa(len(encrypted))),
			"rangestart": req["rangestart"], "rangeend": req["rangeend"], "seq": req["seq"], "filedata": encrypted[start : end+1]}, nil
	}
	got, err := downloadCDN(cdnConfig{}, &accountState{}, desc, post)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("download: %v", err)
	}
	encrypted[0] ^= 1
	if _, err = downloadCDN(cdnConfig{}, &accountState{}, desc, post); err == nil {
		t.Fatal("corrupt download accepted")
	}
}

func TestUploadCDNReassemblesFileAndChecksReply(t *testing.T) {
	data := bytes.Repeat([]byte("file"), 20000)
	var ciphertext []byte
	post := func(req cdnFields) (cdnFields, error) {
		start, err := cdnNumber(req, "rangestart")
		if err != nil || start != len(ciphertext) {
			t.Fatalf("upload offset %d: %v", start, err)
		}
		ciphertext = append(ciphertext, req["filedata"]...)
		return cdnFields{"retcode": []byte("0"), "seq": req["seq"], "fileid": []byte("test-file")}, nil
	}
	file, err := uploadCDNFile(cdnConfig{}, &accountState{}, "test-peer", "файл.txt", "test-id", data, post)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(file.key)
	plain, err := cdnCrypt(ciphertext, file.key, true)
	if err != nil || !bytes.Equal(plain, data) || file.id != "test-file" {
		t.Fatalf("upload: %v", err)
	}
}
