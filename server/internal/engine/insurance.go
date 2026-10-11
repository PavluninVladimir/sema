package engine

// Страховка возврата: перед тем, как менять то, что может отрезать доступ
// (сеть, файрвол, ssh, пользователи), на самом сервере заводится
// отложенный возврат. Он сработает сам, если Сёма не отменит его после
// того, как заново подключился и проверил пробой.
//
// Иначе одна неверная строка в sshd_config закрывает дверь навсегда: с той
// стороны никого нет, а «вернуть» некому. Отсюда правило — команду с
// правилом «Спросить» на сервере, отданном Сёме целиком, выполняем только
// со страховкой, и проверяем **новым** соединением: то, по которому команда
// шла, живёт и после того, как дверь закрылась.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

// insuranceMinKey — на сколько минут заводится возврат. Настройка: у
// домашнего роутера и у сервера с почтой разная цена ошибки.
const insuranceMinKey = "auto.rollback_min"

// InsuranceMinutes — срок, через который сервер вернёт себя сам.
func (e *Engine) InsuranceMinutes() int {
	return e.store.SettingInt(insuranceMinDef)
}

// Insurance — заведённая страховка: чем она снимается и когда сработает.
type Insurance struct {
	Host   string
	Mark   string // метка сторожа на сервере
	Back   string // что вернёт сервер, если её не снять
	Until  time.Time
	Placed bool
}

// insuranceReady — можно ли вообще подстраховать команду на этом сервере.
// Возвращает причину отказа: она же объяснение человеку.
func (e *Engine) insuranceReady(h *store.Host, cmd string) string {
	switch {
	case h.Kind == "routeros":
		return "" // у RouterOS есть планировщик — возврат ставится им
	case h.NoSudo:
		return "страховку возврата ставить нечем: на сервере нет sudo"
	case strings.TrimSpace(h.Kind) == "" || h.Kind == "linux":
		return ""
	}
	return "страховку возврата на сервере этого рода ставить нечем"
}

// placeInsurance — завести отложенный возврат на сервере. Возврат —
// команда, названная арбитром: она выполнится сама, если страховку не
// снять.
func (e *Engine) placeInsurance(ctx context.Context, h *store.Host, back string) (*Insurance, error) {
	back = strings.TrimSpace(back)
	if back == "" {
		return nil, fmt.Errorf("возврат не назван — страховать нечем")
	}
	if why := shellSafety(back); why != "" {
		return nil, fmt.Errorf("возврат непонятного вида (%s) — так страховать нельзя", why)
	}
	mins := e.InsuranceMinutes()
	ins := &Insurance{Host: h.Name, Back: back, Until: time.Now().Add(time.Duration(mins) * time.Minute),
		Mark: "sema-rollback-" + strconv.FormatInt(time.Now().Unix(), 10)}
	var cmd string
	if h.Kind == "routeros" {
		// планировщик RouterOS: одноразовое задание через N минут
		cmd = fmt.Sprintf(`/system scheduler add name=%s interval=%dm on-event="%s ; /system scheduler remove %s"`,
			ins.Mark, mins, strings.ReplaceAll(back, `"`, `\"`), ins.Mark)
	} else {
		// сторож в фоне: ждёт и возвращает. Метка в командной строке —
		// по ней сторожа потом и снимают
		cmd = fmt.Sprintf(`nohup sh -c 'sleep %d; %s; echo %s done' >/dev/null 2>&1 & echo %s placed`,
			mins*60, back, ins.Mark, ins.Mark)
	}
	res, err := e.executeSSH(ctx, h, cmd, "ядро · страховка возврата")
	if err != nil {
		return nil, fmt.Errorf("страховка не завелась: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("страховка не завелась: код %d, %s", res.Code, clip(res.Output, 200))
	}
	ins.Placed = true
	e.store.LogSSH(store.SSHLogEntry{Host: h.Name, Cmd: cmd, Who: "ядро · страховка возврата",
		Decision: store.SSHAuto, Code: res.Code,
		Output: fmt.Sprintf("возврат через %d мин: %s", mins, clip(back, 300))})
	return ins, nil
}

// dropInsurance — снять страховку: сервер проверен, возвращать нечего.
func (e *Engine) dropInsurance(ctx context.Context, h *store.Host, ins *Insurance) error {
	if ins == nil || !ins.Placed {
		return nil
	}
	var cmd string
	if h.Kind == "routeros" {
		cmd = "/system scheduler remove " + ins.Mark
	} else {
		cmd = "pkill -f " + ins.Mark + " ; true"
	}
	res, err := e.executeSSH(ctx, h, cmd, "ядро · снятие страховки")
	if err != nil {
		return fmt.Errorf("страховка не снялась: %w", err)
	}
	ins.Placed = false
	e.store.LogSSH(store.SSHLogEntry{Host: h.Name, Cmd: cmd, Who: "ядро · снятие страховки",
		Decision: store.SSHAuto, Code: res.Code, Output: "проверка сошлась, возврат отменён"})
	return nil
}

// reachable — открывается ли к серверу **новое** соединение. Проверять по
// тому, по которому шла команда, бессмысленно: оно живёт и после того, как
// дверь закрылась для новых.
func (e *Engine) reachable(ctx context.Context, h *store.Host) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe := "echo sema-alive"
	if h.Kind == "routeros" {
		probe = "/system identity print"
	}
	res, err := e.executeSSH(cctx, h, probe, "ядро · связь после изменения")
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("сервер ответил кодом %d", res.Code)
	}
	return nil
}
