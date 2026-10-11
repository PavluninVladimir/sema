package llm

// Свой или чужой адрес. Правило одно на весь сервер: и модель, и эмбеддер
// должны отвечать на этот вопрос одинаково, иначе получается, что письмо
// нельзя отдать модели, но можно — эмбеддеру по тому же адресу.

import (
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var reDockerNet = regexp.MustCompile(`^172\.(1[6-9]|2\d|3[01])\.`)

// HostIsHome — адрес не уходит из своей сети.
//
// Короткое имя без точки — это сосед по сети докера (`llm`, `embed`): в
// интернете таких имён не бывает, публичному узлу нужна точка. Без этой
// строки модель в соседнем контейнере считалась облачной, и Сёма решал,
// что домашней модели у неё нет вовсе.
func HostIsHome(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false // адрес не разобрали — считаем чужим, так безопаснее
	}
	host := u.Hostname()
	if alias := os.Getenv("SEMA_HOST_ALIAS"); alias != "" && host == alias {
		return true // хозяин контейнера: та же машина, просто снаружи докера
	}
	switch host {
	case "localhost", "host.docker.internal", "":
		return true
	}
	if !strings.Contains(host, ".") {
		return true // сосед по сети докера
	}
	if strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || reDockerNet.MatchString(host)
}
