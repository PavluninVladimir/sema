package engine

// Срок хранения журнала изменений настроек. Сам журнал живёт в
// `store/settings_log.go`: писать его движку не нужно — запись стоит в
// той единственной функции, через которую настройка вообще меняется.

import (
	"strconv"
	"time"
)

// SettingsLogKeepKey — сколько дней держим правки. Настройка, а не
// гвоздь: у одного человека журнал нужен на месяц, у другого — на год, и
// оба правы.
const SettingsLogKeepKey = "settings.log_keep_days"

const settingsLogKeepDef = 180

func (e *Engine) SettingsLogKeepDays() int {
	return e.store.SettingInt(settingsLogKeepDaysDef)
}

func (e *Engine) SetSettingsLogKeepDays(n int) error {
	return e.store.SetSetting(SettingsLogKeepKey, strconv.Itoa(n))
}

// SettingsLogPurgePass — уборка по сроку; зовётся раз в час «Пульсом
// сервера» вместе с прочей уборкой журналов.
func (e *Engine) SettingsLogPurgePass() int64 {
	before := time.Now().AddDate(0, 0, -e.SettingsLogKeepDays()).Format(time.RFC3339)
	return e.store.PurgeSettingsLog(before)
}
