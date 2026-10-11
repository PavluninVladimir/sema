package engine

// Память о том, что связи нет. Не ошибка — состояние: с какого момента
// сеть пропала. Пока оно есть, дайджест говорит об этом человеку, а
// очередь и роли не делают из отсутствия Wi-Fi поломку.

import (
	"sync"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/llm"
)

var (
	netMu        sync.Mutex
	netDownSince time.Time
)

// noteNetwork — по исходу каждого вызова модели: сетевая ошибка начинает
// отсчёт, любой удачный ответ его снимает.
func noteNetwork(err error) {
	netMu.Lock()
	defer netMu.Unlock()
	switch {
	case err == nil:
		netDownSince = time.Time{}
	case llm.IsNetworkErr(err) && netDownSince.IsZero():
		netDownSince = time.Now()
	}
}

// NetDown — с какого времени нет связи; false — связь есть или не проверяли.
func (e *Engine) NetDown() (time.Time, bool) {
	netMu.Lock()
	defer netMu.Unlock()
	return netDownSince, !netDownSince.IsZero()
}

// NetRetryDelay — через сколько пробовать снова, когда сети нет. Настройка,
// не гвоздь: у кого связь падает на секунды, тому минута, у кого на ночь —
// незачем стучаться каждые тридцать секунд.
func (e *Engine) NetRetryDelay() time.Duration {
	return time.Duration(e.settingInt("queue.net_retry_min", 5)) * time.Minute
}
