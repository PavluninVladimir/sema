package llm

import "testing"

// Модель и эмбеддер обязаны отвечать одинаково: иначе выходит, что письмо
// нельзя отдать модели, но можно — эмбеддеру по тому же адресу.
func TestHomeAndRemoteAddr(t *testing.T) {
	home := []string{
		"http://127.0.0.1:1234/v1", "http://localhost:8080/v1",
		"http://llm:8080/v1", "http://embed:8080/v1", // соседи по сети докера
		"http://192.168.88.10:11434/v1", "http://10.0.0.5/v1",
		"http://172.17.0.2:8080/v1", "http://sema.local:8765/v1",
		"http://host.docker.internal:1234/v1",
	}
	remote := []string{
		"https://api.openai.com/v1", "https://api.deepseek.com/v1",
		"https://api.perplexity.ai", "http://8.8.8.8/v1",
		"http://172.32.0.1/v1", // соседняя восьмёрка уже не частная
		"не адрес вовсе",
	}
	for _, a := range home {
		if !HostIsHome(a) {
			t.Errorf("%s посчитан чужим", a)
		}
	}
	for _, a := range remote {
		if HostIsHome(a) {
			t.Errorf("%s посчитан своим", a)
		}
	}
}
