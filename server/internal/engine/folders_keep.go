package engine

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// «Оставить» у замечания про зря разбираемые папки.
//
// У замечания была одна кнопка — «Выключить все». Но выключить не всегда
// верно: папка может понадобиться, а ярлык — появиться заново. Человеку
// нужно уметь сказать «знаю, пусть разбирает», и чтобы это не значило
// «молчи навсегда»: список папок у ящика меняется, и завтра в нём может
// оказаться то, чего вчера не было.
//
// Поэтому решение помнится вместе с отпечатком списка: изменился список —
// замечание возвращается сразу; не изменился — возвращается через
// mail.folders_recheck_days, чтобы человек раз в месяц взглянул ещё раз.

const foldersDecisionKey = "mail.folders_ok."

// FolderFingerprint — список папок как одна строка: порядок не важен, важен
// состав.
func FolderFingerprint(folders []string) string {
	backup := append([]string(nil), folders...)
	sort.Strings(backup)
	return strings.Join(backup, "|")
}

// FoldersCheckEvery — через сколько дней спросить снова про тот же
// список папок. 0 — не спрашивать больше никогда.
func (e *Engine) FoldersCheckEvery() int {
	n, err := strconv.Atoi(e.store.Setting("mail.folders_recheck_days", "30"))
	if err != nil || n < 0 {
		return 30
	}
	return n
}

// KeepFolders — человек решил оставить эти папки в разборе.
func (e *Engine) KeepFolders(account string, folders []string) error {
	return e.store.SetSetting(foldersDecisionKey+account,
		time.Now().Format(time.RFC3339)+"|"+FolderFingerprint(folders))
}

// FoldersKept — говорить ли про эти папки. Возвращает «молчать» и,
// когда решение было принято, его дату: замечание, вернувшееся по сроку,
// честно говорит, что человек уже отвечал.
func (e *Engine) FoldersKept(account string, folders []string) (silent bool, when time.Time) {
	v := e.store.Setting(foldersDecisionKey+account, "")
	date, fingerprint, ok := strings.Cut(v, "|")
	if !ok {
		return false, time.Time{}
	}
	was, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return false, time.Time{}
	}
	// состав папок изменился — это уже другой вопрос, и спросить надо
	// сразу, а не через месяц
	if fingerprint != FolderFingerprint(folders) {
		return false, was
	}
	days := e.FoldersCheckEvery()
	if days == 0 {
		return true, was
	}
	return time.Since(was) < time.Duration(days)*24*time.Hour, was
}
