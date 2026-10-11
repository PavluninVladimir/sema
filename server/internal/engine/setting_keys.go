package engine

// Ключи настроек, которые правятся с экрана, но в реестре (`store.SettingDef`) не
// описаны: их читают и пишут сеттеры движка. Экран кладёт ключ в тумблер, список
// или поле (`Component.key`, `Select.key`, `Field.key`), и по нему индекс настроек
// знает строку, а журнал называет правку по-человечески («Ночная смена выполняет
// наряд»), а не ключом: 2 октября 2026 на «Вы меняли» стояли `images.mode`,
// `ocr_engine`, `watch.doctor`, `night.dry_run`.
//
// Строка ключа живёт ровно в одном месте, и экран берёт её отсюда, а не пишет рядом
// ещё раз: расходящийся ключ молча вернул бы журналу сырое имя.
const (
	// WatchDoctorKey — задавать ли вопросы самоосмотра на ходу
	WatchDoctorKey = watchDoctorKey
	// OCREngineKey — чем читать картинки без модели: vision | tesseract | none
	OCREngineKey = ocrEngineKey
	// NightDryRunKey — ночная смена вхолостую: наряд собирается, шаги не выполняются
	NightDryRunKey = nightDryKey
	// WebFindURLKey — адрес SearXNG для поиска страниц
	WebFindURLKey = webFindURLKey
	// ImageModeKey — как выбирается модель для картинок: cheap | better
	ImageModeKey = "images.mode"
	// ModuleTaskTimeoutKey — предел времени задачи модуля, секунды
	ModuleTaskTimeoutKey = "modules.task_timeout_s"
)
