package wechat

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
	"unicode/utf8"
)

type lanListener struct {
	net    *net.IPNet
	socket *net.TCPListener
}

type lanChallenge struct {
	listeners []lanListener
	workers   sync.WaitGroup
	expires   time.Time
	port      int
	frame     []byte
}

func privateIPv4Networks() ([]*net.IPNet, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var networks []*net.IPNet
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if ok && network.IP.To4() != nil && network.IP.IsPrivate() && len(networks) < 64 {
				networks = append(networks, network)
			}
		}
	}
	return networks, nil
}

func bindLAN(expires time.Time) (*lanChallenge, error) {
	networks, err := privateIPv4Networks()
	if err != nil || len(networks) == 0 {
		return nil, errors.New("проверка WeChat недоступна: на сервере нет локального IPv4")
	}
	for port := 8050; port <= 8100 && time.Now().Before(expires); port++ {
		challenge := &lanChallenge{port: port, expires: expires}
		for _, network := range networks {
			socket, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: network.IP, Port: port})
			if err == nil {
				challenge.listeners = append(challenge.listeners, lanListener{net: network, socket: socket})
			}
		}
		if len(challenge.listeners) > 0 {
			return challenge, nil
		}
	}
	return nil, errors.New("проверка WeChat недоступна: не удалось открыть временный порт в локальной сети")
}

func (l *lanChallenge) close() {
	for _, listener := range l.listeners {
		listener.socket.Close()
	}
	l.workers.Wait()
	clear(l.frame)
}

func (l *lanChallenge) serve(listener lanListener) {
	defer l.workers.Done()
	defer listener.socket.Close()
	if listener.socket.SetDeadline(l.expires) != nil {
		return
	}
	for time.Now().Before(l.expires) {
		conn, err := listener.socket.AcceptTCP()
		if err != nil {
			return
		}
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if ok && peer.IP.IsPrivate() && listener.net.Contains(peer.IP) &&
			conn.SetWriteDeadline(time.Now().Add(time.Second)) == nil {
			_, _ = conn.Write(l.frame)
		}
		conn.Close()
	}
}

func makeLANRequest(device loginDevice, identifier []byte, addresses []net.IP, port int) ([]byte, error) {
	if len(identifier) == 0 || len(identifier) > 4096 || len(addresses) == 0 || len(addresses) > 64 || port < 1 || port > 65535 {
		return nil, errors.New("неверные параметры локальной проверки WeChat")
	}
	base, err := baseRequest(device, 0, 0)
	if err != nil {
		return nil, err
	}
	var entries []byte
	for _, address := range addresses {
		entry, _ := appendBytes(nil, 1, []byte(address.String()))
		entry, _ = appendUint(entry, 2, uint64(port))
		entries, _ = appendBytes(entries, 1, entry)
	}
	nonce, err := loginNonceBuffer()
	if err != nil {
		return nil, err
	}
	b, _ := appendBytes(nil, 1, base)
	b, _ = appendBytes(b, 2, entries)
	b, _ = appendBytes(b, 3, identifier)
	return appendBytes(b, 4, nonce)
}

func decodeLANFrame(reply []byte) ([]byte, error) {
	if len(reply) > 16384 {
		return nil, errors.New("слишком большой ответ локальной проверки WeChat")
	}
	fields, err := loginResponseFields(reply, "подтверждение в локальной сети")
	if err != nil {
		return nil, err
	}
	token, err := dataField(fields, 2)
	if err != nil || len(token) == 0 || len(token) > 4096 || !utf8.Valid(token) {
		return nil, errors.New("неверное подтверждение локальной проверки WeChat")
	}
	defer clear(token)
	body, err := json.Marshal(struct {
		Scene string `json:"scene"`
		Token string `json:"token"`
	}{"xwechat", string(token)})
	if err != nil {
		return nil, err
	}
	defer clear(body)
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body)+4)), body...), nil
}

func (a *qrAttempt) startLAN(ctx context.Context) error {
	if a.lan != nil {
		return nil
	}
	challenge, err := bindLAN(a.expires)
	if err != nil {
		return err
	}
	defer func() {
		if a.lan == nil {
			challenge.close()
		}
	}()
	addresses := make([]net.IP, 0, len(challenge.listeners))
	for _, listener := range challenge.listeners {
		addresses = append(addresses, listener.net.IP)
	}
	request, err := makeLANRequest(a.device, a.qr.identifier, addresses, challenge.port)
	if err != nil {
		return err
	}
	defer clear(request)
	ctx, cancel := context.WithDeadline(ctx, a.expires)
	defer cancel()
	reply, cookie, err := a.session.preloginRPC(ctx, defaultShortHost, 22312, "/cgi-bin/micromsg-bin/extdeviceuploadlaninfo", request, a.cookie, nil)
	if err != nil {
		return err
	}
	defer clear(reply)
	frame, err := decodeLANFrame(reply)
	if err != nil {
		return err
	}
	if len(cookie) != 0 {
		clear(a.cookie)
		a.cookie = cookie
	}
	challenge.frame = frame
	for _, listener := range challenge.listeners {
		challenge.workers.Add(1)
		go challenge.serve(listener)
	}
	a.lan = challenge
	return nil
}
