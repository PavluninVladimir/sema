package wechat

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
	"time"
)

func TestMMTLSHandshake(t *testing.T) {
	for _, good := range []bool{true, false} {
		name := "signature rejected"
		if good {
			name = "authenticated session"
		}
		t.Run(name, func(t *testing.T) {
			private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			pin := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				// Independent fake peer uses a deterministic server scalar.
				send := func(kind byte, body []byte, keys *mmtlsTraffic, seq uint64) bool {
					var frame []byte
					var err error
					if keys == nil {
						frame, err = mmtlsFrame(kind, body)
					} else {
						frame, err = sealMMTLS(kind, body, keys.serverKey, keys.serverIV, seq)
					}
					if err == nil {
						err = writeMMTLSRecord(server, frame)
					}
					if err != nil {
						t.Error(err)
					}
					return err == nil
				}
				r, err := readMMTLSRecord(server)
				if err != nil {
					t.Error(err)
					return
				}
				// Prefix, lengths, extension number, share length and key id precede the point.
				if len(r.body) < 128 || !bytes.Equal(r.body[4:10], []byte{1, 4, 0xf1, 1, 0xc0, 0x2b}) {
					t.Error("bad ClientHello")
					return
				}
				c := handshakeCursor{r.body[4+6+32+4:]}
				ext, err := c.sized(4)
				if err != nil {
					t.Error(err)
					return
				}
				e := handshakeCursor{ext[1:]}
				offer, err := e.sized(4)
				if err != nil {
					t.Error(err)
					return
				}
				o := handshakeCursor{offer[3:]}
				share, err := o.sized(4)
				if err != nil {
					t.Error(err)
					return
				}
				p := handshakeCursor{share[4:]}
				point, err := p.sized(2)
				if err != nil {
					t.Error(err)
					return
				}
				key, err := protocolKeyFromPrivate(elliptic.P256(), []byte{9})
				if err != nil {
					t.Error(err)
					return
				}
				secretBytes, err := key.derive(point)
				if err != nil {
					t.Error(err)
					return
				}
				secret := sha256.Sum256(secretBytes)
				serverShare := append([]byte{0, 0x11, 0, 0, 0, 5}, mustSized(t, key.public())...)
				serverCert := []byte{0, 0x13, 0, 0, 0, 1, 0, 0, 0, 5}
				extensions := append([]byte{2}, mustSection(t, serverShare)...)
				extensions = append(extensions, mustSection(t, serverCert)...)
				hello := append([]byte{2, 4, 0xf1, 0xc0, 0x2b}, make([]byte, 32)...)
				hello = mustSection(t, append(hello, mustSection(t, extensions)...))
				if !send(0x16, hello, nil, 0) {
					return
				}
				transcript := append(r.body, hello...)
				traffic, err := deriveMMTLSTraffic(secret[:], "handshake key expansion", transcript)
				if err != nil {
					t.Error(err)
					return
				}
				digest := sha256.Sum256(transcript)
				digest = sha256.Sum256(digest[:])
				signature, err := ecdsa.SignASN1(rand.Reader, private, digest[:])
				if err != nil {
					t.Error(err)
					return
				}
				if !good {
					signature[len(signature)-1] ^= 1
				}
				signed := mustSection(t, append([]byte{0x0f}, mustSized(t, signature)...))
				if !send(0x16, signed, &traffic, 1) || !good {
					return
				}
				transcript = append(transcript, signed...)
				ticket := []byte{1, 0, 0, 0, 30, 0, 0, 0, 0, 0, 7}
				ticket = append(ticket, mustSized(t, make([]byte, 12))...)
				ticket = append(ticket, mustSized(t, []byte{9, 8, 7})...)
				tickets := mustSection(t, append([]byte{4, 1}, mustSection(t, ticket)...))
				if !send(0x16, tickets, &traffic, 2) {
					return
				}
				transcript = append(transcript, tickets...)
				finish, err := finishedMAC(secret[:], "server finished", transcript)
				if err != nil {
					t.Error(err)
					return
				}
				if !send(0x16, mustSection(t, append([]byte{0x14}, mustSized(t, finish)...)), &traffic, 3) {
					return
				}
				r, err = readMMTLSRecord(server)
				if err != nil {
					t.Error(err)
					return
				}
				body, err := openMMTLS(r, traffic.clientKey, traffic.clientIV, 1)
				if err != nil {
					t.Error(err)
					return
				}
				got, err := signedHandshakePayload(body, 0x14)
				want, _ := finishedMAC(secret[:], "client finished", transcript)
				if err != nil || !bytes.Equal(got, want) {
					t.Error("bad client Finished", err)
					return
				}
				material, _ := transcriptKey(secret[:], "expanded secret", transcript, 32)
				app, _ := deriveMMTLSTraffic(material, "application data key expansion", transcript)
				r, err = readMMTLSRecord(server)
				if err != nil {
					t.Error(err)
					return
				}
				body, err = openMMTLS(r, app.clientKey, app.clientIV, 2)
				if err != nil {
					t.Error(err)
					return
				}
				packet, err := unpackLonglink(body)
				if err != nil || packet.command != 6 || packet.sequence != ^uint32(0) || len(packet.payload) != 0 {
					t.Error("bad NOOP", err)
					return
				}
				noop, _ := packLonglink(0x3b9aca06, ^uint32(0), nil)
				send(0x17, noop, &app, 4)
			}()
			session, err := authenticateMMTLS(ctx, client, map[uint32]string{5: pin})
			if good {
				if err != nil {
					t.Error(err)
				} else if !bytes.Equal(session.ticket.opaque, []byte{9, 8, 7}) || len(session.accessKey) != 32 {
					t.Error("invalid session")
				}
			} else if err == nil {
				t.Error("invalid server signature accepted")
			}
			client.Close()
			<-done
		})
	}
}
