package engine

// Проверка 5: две свои записи спорят.
//
// 11 сентября 2026 четыре копии подряд были объявлены неуехавшими, и все
// четыре в этот момент лежали на Яндекс.Диске целыми. Яндекс принимал 38 МБ
// до последнего байта, сохранял файл и закрывал соединение, не ответив; Сёма
// видела EOF, писала тревогу и снимала копию заново. Сверено хэшами: у
// копии, объявленной «не уехала», sha256 на Диске совпал с тем, что Сёма
// записала при снятии.
//
// Обе стороны Сёма знала сама. Никто их не сравнил.
//
// Эта проверка — единственная из пяти, которая ходит в сеть, и потому живёт
// по особым правилам: своё окно (раз в час, а не раз в пять минут), свой
// выключатель и запрет ставить паузы. Сторож, который лезет в сеть каждую
// минуту, сам становится нагрузкой, а остановить работу по итогам чужого
// ответа — слишком много власти для проверки, которая зависит от чужой
// доступности.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/store"
)

const (
	watchDisagreeKey  = "watch.disagree"      // ходить ли в сеть за сверкой
	watchDisagreeHKey = "watch.disagree_h"    // как часто, часов
	watchDisagreeWhen = "watch.disagree.last" // когда ходили в прошлый раз
)

// WatchDisagree — сверять ли свои итоги с хранилищем. По умолчанию да:
// проверка нашла бы ложную тревогу, которая стоила человеку полдня.
func (e *Engine) WatchDisagree() bool { return e.store.Setting(watchDisagreeKey, "1") == "1" }

func (e *Engine) SetWatchDisagree(on bool) error {
	return e.store.SetSetting(watchDisagreeKey, boolSetting(on))
}

// backupName — «копия сёма-2026-09-11-1401.tgz не уехала» и прочие итоги
// задачи, где имя файла названо.
var backupName = regexp.MustCompile(`(сёма-[0-9\-]+\.tgz)`)

// checkDispute — свой итог против своего же хранилища.
//
// Пока сверяем одну пару: итог «Копии наружу» против Яндекс.Диска. Пара
// выбрана не случайно — это единственное место, где Сёма объявляет неудачу,
// не спросив ту сторону, о которой говорит.
func (e *Engine) checkDispute(ctx context.Context) int {
	if !e.WatchDisagree() {
		return 0
	}
	// своё окно: в сеть не ходим чаще раза в час
	every := time.Duration(e.settingInt(watchDisagreeHKey, 1)) * time.Hour
	res, _ := time.Parse(time.RFC3339, e.store.Setting(watchDisagreeWhen, ""))
	if time.Since(res) < every {
		return 0
	}

	t, err := e.taskBySystem(WorkerBackup)
	if err != nil || t == nil || t.LastResult == "" {
		return 0
	}
	key := "спор:копия"
	// говорим только о неудаче: успех сверять незачем
	if !strings.Contains(t.LastResult, "не уехала") && !strings.Contains(t.LastResult, "встала") {
		e.closeIfWas(key, "копия уехала")
		return 0
	}
	m := backupName.FindStringSubmatch(t.LastResult)
	if m == nil {
		return 0
	}
	name := m[1]

	d, folder, err := e.diskBackupsAPI()
	if err != nil || d == nil {
		return 0 // копии едут не родным API — спросить не у кого
	}
	_ = e.store.SetSetting(watchDisagreeWhen, time.Now().Format(time.RFC3339))

	ask, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	z, err := d.Stat(ask, folder+"/"+name)
	if err != nil {
		// Диска нет или файла нет — тогда итог задачи правдив, и спорить
		// не о чем. Молчание здесь важно: проверка, которая шумит при
		// недоступной сети, научит не читать её вовсе
		e.closeIfWas(key, "на Диске копии действительно нет")
		return 0
	}
	if z.Size <= 0 {
		return 0
	}
	e.sayAboutFinding(store.Finding{
		Key: key, Kind: "две записи спорят", Ref: name,
		Level:   store.FindingWarn,
		Summary: fmt.Sprintf("копия объявлена неуехавшей, а на Диске она лежит: %s", name),
		Numbers: fmt.Sprintf("на Диске %s, sha256 %s · итог задачи: %s",
			humanBytes(z.Size), shortHash(z.Hash), clip(t.LastResult, 90)),
		// паузы эта проверка не ставит никогда: она зависит от чужой
		// доступности, и такой власти у неё быть не должно
		Measure: store.MeasureAlert, Screen: "settings:storages",
	})
	return 1
}
