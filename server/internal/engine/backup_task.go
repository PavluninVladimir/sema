package engine

// Копия наружу. До сегодняшнего дня резервной копии у Сёмы не было
// вовсе: `sema -backup` — ручной флаг, и ни одна задача его не звала.
// Вся Сёма — база на 120 МБ с письмами, памятью, ключами и сессиями —
// жила в одном экземпляре на одной карточке в Raspberry Pi.
//
// Задача снимает копию, проверяет её самоосмотром (тем же, что и
// `-restore`, только без замка и без подмены базы), выгружает на Диск
// потоком и убирает старые. Порядок именно такой: непроверенную копию
// выгружать незачем, а старую убирать до того, как новая доехала, —
// значит на минуту остаться вовсе без копий.

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/backup"
	"github.com/PavluninVladimir/sema/server/internal/host"
	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	WorkerBackup = "backup"

	backupOnKey    = "backup.on"
	backupKeepKey  = "backup.keep"
	backupFilesKey = "backup.with_files"
	backupNameHead = "сёма-"
)

// BackupOn — снимать ли копию по расписанию. По умолчанию нет: копия
// уезжает наружу, и человек включает это сам, назвав куда.
func (e *Engine) BackupOn() bool { return e.store.Setting(backupOnKey, "0") == "1" }

// SetBackupOn — один выключатель, а не два. До 10 сентября 2026 настройка
// и сама задача сердцебиения жили порознь: человек включал «Копию наружу»
// на «Хранилищах», настройка становилась «1», а задача оставалась
// выключенной — и копия не снималась. Никакой ошибки при этом не было, на
// экране стояло «включено», и узнать правду было неоткуда.
//
// Настройка осталась (ей проверяется сам проход), но щёлкает она обе.
func (e *Engine) SetBackupOn(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	if err := e.store.SetSetting(backupOnKey, v); err != nil {
		return err
	}
	return e.enableTask(WorkerBackup, on)
}

// BackupTaskOn — включена ли сама задача. Экран показывает расхождение,
// если оно вдруг появится: молчащая копия — худший вид молчания.
func (e *Engine) BackupTaskOn() bool { return e.taskEnabled(WorkerBackup) }

// BackupKeep — сколько копий держать на Диске.
func (e *Engine) BackupKeep() int {
	v, err := strconv.Atoi(e.store.Setting(backupKeepKey, "7"))
	if err != nil || v < 1 {
		return 7
	}
	return v
}

// BackupWithFiles — класть ли файлы в архив. По умолчанию нет: файлы
// уезжают зеркалом, а копия базы в разы легче и доедет по слабому
// каналу. Опись честно называет, чего в архиве нет.
func (e *Engine) BackupWithFiles() bool { return e.store.Setting(backupFilesKey, "0") == "1" }

// backupPass — задача «Копия наружу».
func (e *Engine) backupPass(ctx context.Context) string {
	if !e.BackupOn() {
		return "копия по расписанию выключена"
	}
	api, folderAPI, err := e.diskBackupsAPI()
	if err != nil {
		return "некуда складывать: " + err.Error()
	}
	if api == nil {
		return "некуда складывать: у Яндекс.Диска не назван секрет с токеном — задайте его на «Хранилищах» кнопкой «Токен для копий»"
	}
	// на снятие нужно место: VACUUM INTO делает второй экземпляр базы,
	// а поверх ложится архив. Не влезет — лучше сказать заранее, чем
	// упасть на середине и оставить хвост во временном каталоге
	if free := host.DiskFree(e.conf.Data); free > 0 {
		if need := dbSize(e) * 2; free < need {
			trouble := fmt.Sprintf("на диске %s, а копии нужно около %s", humanBytes(free), humanBytes(need))
			e.sayAboutBackup("Копия не снята: мало места", trouble, "alert")
			return trouble
		}
	}

	tmp, err := os.MkdirTemp(e.conf.Data, ".копия-*")
	if err != nil {
		return "не создать временный каталог: " + err.Error()
	}
	defer os.RemoveAll(tmp)

	name := backupNameHead + time.Now().Format("2006-01-02-1504") + ".tgz"
	path := filepath.Join(tmp, name)
	if err := backup.Make(e.conf, path, !e.BackupWithFiles()); err != nil {
		e.sayAboutBackup("Копия не снялась", err.Error(), "alert")
		return "не снялась: " + err.Error()
	}
	// самоосмотр до выгрузки: непроверенную копию везти незачем
	if _, dir, err := backup.Inspect(path, os.Getenv(e.conf.KeyEnv), tmp); err != nil {
		_ = os.RemoveAll(dir)
		e.sayAboutBackup("Копия не прошла проверку", err.Error(), "alert")
		return "не прошла проверку: " + err.Error()
	} else {
		_ = os.RemoveAll(dir)
	}

	size := backupFileSize(path)
	// свой срок: узел загрузки молчащим не бывает, а канал бывает
	ctxAPI, cancelAPI := context.WithTimeout(ctx, uploadDeadline(size, e.MinSpeed()))
	result, err := e.takeBackup(ctxAPI, api, folderAPI, path, name)
	cancelAPI()
	if err != nil {
		e.sayAboutBackup("Копия не уехала на Диск", err.Error(), "alert")
		return "не уехала: " + err.Error()
	}
	if removed := e.removeOldAPI(ctx, api, folderAPI); removed > 0 {
		result += fmt.Sprintf(" · убрано старых %d", removed)
	}
	_ = e.store.SetSetting("backup.last", time.Now().Format(time.RFC3339))
	_ = e.store.SetSetting("backup.last_size", strconv.FormatInt(size, 10))
	e.closeAboutBackup()
	return result
}

// MinSpeed — байт в секунду, ниже которой выгрузку считаем
// вставшей. Настройка, а не гвоздь: у кого-то канал в мегабит, у
// кого-то в сто, и «медленно» у них разное.
func (e *Engine) MinSpeed() int64 {
	if v, err := strconv.Atoi(e.store.Setting("backup.min_bps", "")); err == nil && v > 0 {
		return int64(v)
	}
	return 20 << 10 // 20 КБ/с: заведомо меньше любого домашнего канала
}

// uploadDeadline — сколько ждать файл такого размера при такой скорости.
// Снизу подпираем пятью минутами: короткая копия на медленном канале
// иначе оборвётся на ровном месте.
func uploadDeadline(size, byteInSec int64) time.Duration {
	if byteInSec <= 0 {
		byteInSec = 20 << 10
	}
	s := time.Duration(size/byteInSec+1) * time.Second
	if s < 5*time.Minute {
		return 5 * time.Minute
	}
	return s
}

// uploadPass — сколько байт уже ушло. Пишет строку в журнал раз в шаг.
type uploadPass struct {
	total, sent int64
	start       time.Time
	last        time.Time
	step        time.Duration
}

func (x *uploadPass) Write(p []byte) (int, error) {
	x.sent += int64(len(p))
	if time.Since(x.last) >= x.step {
		x.last = time.Now()
		log.Printf("копия уходит: %s из %s, %s", humanBytes(x.sent), humanBytes(x.total), x.speed())
	}
	return len(p), nil
}

func (x *uploadPass) speed() string {
	s := time.Since(x.start).Seconds()
	if s <= 0 {
		return "скорость неизвестна"
	}
	return humanBytes(int64(float64(x.sent)/s)) + "/с"
}

func (x *uploadPass) result(than string) {
	log.Printf("копия %s: %s из %s за %s, %s", than, humanBytes(x.sent), humanBytes(x.total),
		time.Since(x.start).Round(time.Second), x.speed())
}

// sayAboutBackup — беда с копией видна человеку, а не только в журнале:
// молчащая резервная копия — худший вид молчания.
func (e *Engine) sayAboutBackup(title, result, level string) {
	_, _ = e.Notify(&store.Notification{
		Key: "backup", Source: "core", Level: level, Title: title, Summary: result,
		Actions: []store.NotifyAction{{Label: "Хранилища", Kind: "goto", Screen: "settings:storages"}},
	})
}

func (e *Engine) closeAboutBackup() {
	e.CloseNotification("backup", store.NotifyDone, "копия уехала")
}

func backupFileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}

func dbSize(e *Engine) int64 {
	return backupFileSize(backup.DBPath(e.conf))
}
