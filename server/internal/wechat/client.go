package wechat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const deviceVersion = -227271398

// Login keeps the temporary QR challenge and its secrets in memory.
type Login struct {
	attempt  *qrAttempt
	name     string
	finished bool
}

func BeginLogin(ctx context.Context) (*Login, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := []byte(("l" + hex.EncodeToString(random[:]))[:16])
	const name = "Sema"
	attempt, err := startQRLogin(ctx, loginDevice{id: id, kind: "UnifiedPCLinux x86_64", version: deviceVersion}, name)
	if err != nil {
		return nil, err
	}
	return &Login{attempt: attempt, name: name}, nil
}

func (l *Login) QR() ([]byte, time.Time) {
	return append([]byte(nil), l.attempt.qr.image...), l.attempt.expires
}

func (l *Login) Close() error {
	l.finished = true
	return l.attempt.close()
}

type LoginState string

const (
	LoginWaiting     LoginState = "waiting"
	LoginScanned     LoginState = "scanned"
	LoginLANRequired LoginState = "lan-required"
	LoginRejected    LoginState = "rejected"
	LoginConnected   LoginState = "connected"
)

// Poll returns the session snapshot only after WeChat completes authentication.
// It must be serialized with Close and other Poll calls by the owner of Login.
func (l *Login) Poll(ctx context.Context) (LoginState, []byte, error) {
	if l.finished {
		return "", nil, errors.New("попытка входа WeChat завершена")
	}
	response, err := l.attempt.poll(ctx)
	if err != nil {
		return "", nil, err
	}
	defer clear(response.credential3)
	defer clear(response.credential4)
	if response.lanChallenge && response.status != nil && *response.status <= 2 {
		if err := l.attempt.startLAN(ctx); err != nil {
			l.finished = true
			return "", nil, err
		}
		return LoginLANRequired, nil, nil
	}
	if response.status == nil || *response.status == 0 {
		return LoginWaiting, nil, nil
	}
	if *response.status == 1 {
		return LoginScanned, nil, nil
	}
	if *response.status != 2 {
		l.finished = true
		l.attempt.closeLAN()
		return LoginRejected, nil, nil
	}
	// Do not submit the manual authentication request twice after an ambiguous error.
	l.finished = true
	l.attempt.closeLAN()
	state, err := l.attempt.complete(ctx, l.name, response)
	if err != nil {
		return "", nil, err
	}
	defer state.close()
	secret, err := state.secretSnapshot()
	if err != nil {
		return "", nil, err
	}
	return LoginConnected, secret, nil
}

func readSecretSnapshot(data []byte) (*accountState, error) {
	if len(data) == 0 || len(data) > 1024*1024 {
		return nil, errors.New("недопустимый размер сессии WeChat")
	}
	var s accountState
	if err := json.Unmarshal(data, &s); err != nil {
		s.close()
		return nil, errors.New("повреждена сессия WeChat")
	}
	if s.LongHost == "" {
		s.LongHost = defaultLongHost
	}
	if s.ShortHost == "" {
		s.ShortHost = defaultShortHost
	}
	if !validWeChatHost(s.LongHost) || !validWeChatHost(s.ShortHost) || s.UIN == 0 || s.Username == "" || len(s.DeviceID) != 16 || s.ClientVersion != deviceVersion ||
		(len(s.SessionKey) != 16 && (len(s.RequestKey) != 24 || len(s.ResponseKey) != 24)) {
		s.close()
		return nil, errors.New("неполная сессия WeChat")
	}
	return &s, nil
}

type Message struct {
	Peer, ID, Sender string
	Kind             int32
	Outgoing         bool
	Body             []byte
	At               time.Time
}

type Batch struct {
	Contacts []Contact
	Cursor   []byte
	Messages []Message
	More     bool
}

func AccountIdentity(snapshot []byte) (string, error) {
	state, err := readSecretSnapshot(snapshot)
	if err != nil {
		return "", err
	}
	defer state.close()
	return state.Username, nil
}

// Pull fetches one batch. The caller owns persistence of the returned cursor
// and must commit it only after processing the corresponding messages.
func Pull(ctx context.Context, snapshot, cursor []byte) (Batch, []byte, error) {
	state, err := readSecretSnapshot(snapshot)
	if err != nil {
		return Batch{}, nil, err
	}
	defer state.close()
	session, err := dialMMTLS(ctx, state.LongHost)
	if err != nil {
		return Batch{}, nil, err
	}
	defer session.close()
	batch, cookie, err := session.sync(ctx, state, cursor, 6)
	if err != nil {
		return Batch{}, nil, err
	}
	result := Batch{Cursor: batch.cursor, More: batch.continuation}
	for _, command := range batch.commands {
		if command.id == 2 {
			contact, err := decodeContact(command.payload)
			if err != nil {
				return Batch{}, nil, err
			}
			result.Contacts = append(result.Contacts, contact)
			continue
		}
		if command.id != 5 {
			continue
		}
		m, err := decodeMessage(command.payload, state.Username)
		if err != nil {
			return Batch{}, nil, err
		}
		result.Messages = append(result.Messages, Message{Peer: m.peer, ID: m.archiveID, Sender: m.sender,
			Kind: m.kind, Outgoing: m.outgoing, Body: m.content, At: time.Unix(int64(m.created), 0).UTC()})
	}
	if len(cookie) == 0 {
		return result, nil, nil
	}
	clear(state.Cookie)
	state.Cookie = append([]byte(nil), cookie...)
	updated, err := state.secretSnapshot()
	if err != nil {
		return Batch{}, nil, err
	}
	return result, updated, nil
}

type Delivery struct {
	ServerID string
	Rejected bool
	Code     int32
}

// SendText sends once. begin records the attempt before the request can reach
// WeChat. An ambiguous result must stay unknown and must not trigger a retry.
func SendText(ctx context.Context, snapshot []byte, destination, body string, clientID uint32, begin func() error) (Delivery, error) {
	state, err := readSecretSnapshot(snapshot)
	if err != nil {
		return Delivery{}, err
	}
	defer state.close()
	if _, err = makeTextSend(destination, body, uint32(time.Now().Unix()), clientID); err != nil {
		return Delivery{}, err
	}
	session, err := dialMMTLS(ctx, state.LongHost)
	if err != nil {
		return Delivery{}, err
	}
	defer session.close()
	if err = begin(); err != nil {
		return Delivery{}, err
	}
	result, _, err := session.sendText(ctx, state, destination, body, uint32(time.Now().Unix()), clientID)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{ServerID: result.serverID, Rejected: result.code != 0, Code: result.code}, nil
}
