package engine

// Умолчания редких настроек движка — одним списком.
//
// Сюда вынесены настройки, которые на экранах живут в «Тонкой настройке»:
// пределы, которые правят раз в год. Описание (`store.SettingDef`) несёт
// ключ, вид, умолчание и границы; читатель берёт значение через него
// (`e.store.SettingInt(...)`), а экран спрашивает тот же реестр по ключу
// (`store.LookupSetting`). Число, вписанное рядом с чтением, здесь больше
// не живёт: подпись «всё по умолчанию» на экране считается от этих же
// описаний и не может разойтись с тем, что видит код.
//
// Имена ключей — константы рядом с теми, кто настройку использует; здесь
// они только собраны с умолчаниями.

import "github.com/PavluninVladimir/sema/server/internal/store"

const sshConfirmTimeoutKey = "ssh.confirm_timeout_s"

var (
	// ---- серверы MCP в чате (mcp_chat.go, mcp_error.go, mcp_retry.go) ----
	mcpChatCharsDef     = store.DefPositive(mcpChatCharsKey, 8000, 500, 100000)
	mcpChatCallsDef     = store.DefPositive(mcpChatCallsKey, 3, 1, 10)
	mcpChatResultDef    = store.DefPositive(mcpChatResultKey, 6000, 200, 100000)
	mcpCardCharsDef     = store.DefPositive(mcpCardCharsKey, 20000, 200, 200000)
	mcpRecheckDef       = store.DefCount(mcpRecheckKey, 300, 0, 86400)
	mcpErrorCharsDef    = store.DefPositive(mcpErrorCharsKey, 1500, 200, 20000)
	mcpArgRetriesDef    = store.DefCount(mcpArgRetriesKey, 1, 0, 5)
	mcpToolHintDaysDef  = store.DefCount(mcpToolHintDaysKey, 30, 0, 365)
	mcpToolHintCharsDef = store.DefPositive(mcpToolHintCharsKey, 300, 50, 2000)
	// выжимка длинного списка для модели (chat_steps.go)
	mcpChatListFieldsDef = store.DefText(mcpChatListFieldsKey, mcpListFieldsDefault)
	// ---- вход на серверы MCP (mcp_oauth.go) ----
	// это вход, а не удобство: осторожные, с Обзора не правятся (Careful)
	mcpLoginTTLDef       = store.Careful(store.DefPositive(mcpLoginTTLKey, 10, 1, 120))
	mcpRefreshSkewDef    = store.Careful(store.DefPositive(mcpRefreshSkewKey, 60, 5, 3600))
	mcpNetTimeoutDef     = store.Careful(store.DefPositive(mcpNetTimeoutKey, 20, 3, 120))
	mcpTokenRetriesDef   = store.Careful(store.DefCount(mcpTokenRetriesKey, 3, 0, 5))
	mcpTokenPauseDef     = store.Careful(store.DefCount(mcpTokenPauseKey, 1000, 0, 10000))
	mcpClientMetadataDef = store.Careful(store.DefFlag(mcpClientMetadataKey, true))

	// ---- поиск и чтение страниц (chat_links.go, search.go, web_*.go) ----
	readLinksDef      = store.DefCount(readLinksKey, 3, 0, 10)
	savePagesDef      = store.DefCount(savePagesKey, 0, 0, 10)
	readLinksCharsDef = store.DefPositive(readLinksCharsKey, lastMsgLimit, 1000, 100000)
	readLinksTTLDef   = store.DefCount(readLinksTTLKey, 24, 0, 8760)
	pageCharsDef      = store.DefPositive(pageCharsKey, pageCharsDefault, 1000, pageCharsMax)
	pageLinksDef      = store.DefCount(pageLinksKey, pageLinksDefault, 0, pageLinksMax)
	pageMinCharsDef   = store.DefCount(pageMinCharsKey, pageMinCharsDefault, 0, 5000)
	webFindDayDef     = store.DefPositive(webFindDayKey, webFindDayDefault, 1, 10000)
	webFindGapDef     = store.DefCount(webFindGapKey, webFindGapDefault, 0, 60000)
	webFindCacheDef   = store.DefCount(webFindCacheKey, webFindCacheDefault, 0, 10080)

	// ---- серверы: ночная смена, страховка, SSH (night_shift.go, insurance.go,
	// arbiter.go, ssh.go) ----
	// самостоятельность серверов и их доступ — осторожные (Careful): «с ходу» с
	// Обзора их не правят, даже если человек менял их чаще всего
	nightMaxStepsDef     = store.Careful(store.DefPositive(nightMaxStepsKey, 10, 1, 100))
	nightLookMaxDef      = store.Careful(store.DefPositive(nightLookMaxKey, 12, 1, 200))
	nightLookTimeoutDef  = store.Careful(store.DefPositive(nightLookTimeoutKey, 180, 1, 7200))
	nightEveryDef        = store.Careful(store.DefPositive(nightEveryKey, 24, 1, 8760))
	nightSkillLinuxDef   = store.Careful(store.DefText(nightSkillKey+store.HostLinux, "linux-admin"))
	nightSkillRouterDef  = store.Careful(store.DefText(nightSkillKey+store.HostRouterOS, "mikrotik"))
	insuranceMinDef      = store.Careful(store.DefPositive(insuranceMinKey, 5, 1, 10080))
	autoMaxHourDef       = store.Careful(store.DefPositive(autoMaxHourKey, 3, 1, 1000))
	sshTimeoutDef        = store.Careful(store.DefPositive(sshTimeoutKey, 60, 1, 86400))
	sshConfirmTimeoutDef = store.Careful(store.DefPositive(sshConfirmTimeoutKey, 600, 1, 86400))
	sshMaxOutputDef      = store.Careful(store.DefPositive(sshMaxOutputKey, 20000, 100, 5000000))

	// ---- модели и речь (models.go, price_watch.go, speech.go): «Тонкая
	// настройка» вкладок «Провайдеры» и «Роли» ----
	// предел ответа модели: 0 законен — у одних провайдеров он обязателен, у
	// других мешает
	modelsMaxOutputDef = store.DefCount(modelsMaxOutputKey, 8192, 0, 0)
	priceSkipDef       = store.DefText(priceSkipKey, priceSkipDefault)
	// чьи голосовые уходят облачной модели — это доверие: осторожная (Careful)
	speechOutsideDef = store.Careful(store.DefText(speechOutsideKey, speechOwn))
	// язык: пустая строка — осознанный выбор «пусть модель определит сама», и
	// от «не задано» её отличает только отсутствие строки (SettingText это
	// различает так же, как прежнее чтение)
	speechLangDef  = store.DefText(speechLangKey, defaultSpeechLang)
	speechFilesDef = store.DefText(speechFilesKey, speechFilesUpload)

	// ---- почта: «Тонкая настройка» вкладки «Спам» (engine.go, money.go) ----
	// 0 законен — «не учиться у спама провайдера»
	spamServerVotesDef = store.DefCount(spamServerVotesKey, 3, 0, 50)
	moneyWordsDef      = store.DefText(moneyWordsKey, moneyWordsSeed)

	// ---- пуши на устройства (push.go): «Тонкая настройка» вкладки «Каналы
	// доставки» в «Уведомлениях» ----
	// адрес для связи пустой — значит «брать адрес входа», а если и его нет —
	// запасной (PushSubject)
	pushSubjectDef  = store.DefText(pushSubjectKey, "")
	pushTTLDef      = store.DefPositive(pushTTLKey, 60, 1, 40320)
	pushTTLAlertDef = store.DefPositive(pushTTLAlertKey, 1440, 1, 40320)
	pushStickyDef   = store.DefFlag(pushStickyKey, false)
	// профилировщик Go (perf-pass 1.3): по умолчанию выключен — сужение
	// доступа настройкой, а не кодом; осторожная, в «Под рукой» не встаёт
	debugPprofDef = store.Careful(store.DefFlag(DebugPprofKey, false))
	// журнал медленных запросов (perf-pass 9.4): самый долгий запрос экрана
	// дольше порога — строка в журнале сервера; 0 — не писать
	slowSQLDef = store.DefCount(SlowSQLKey, 50, 0, 10000)
	// первая порция длинного списка (perf-pass 5.1): дальше «Показать ещё»
	listRowsDef = store.DefPositive(ListRowsKey, 20, 5, 500)
	// сколько живёт экран, подгруженный заранее при наведении (perf-pass 7.3);
	// 0 — предзагрузки нет
	prefetchMsDef = store.DefCount(PrefetchMsKey, 5000, 0, 60000)
	// открытый порт ведёт страницы на защищённый, когда тот поднялся (perf-pass
	// 7.4); меняет, куда попадает закладка, — осторожная
	redirectTLSDef = store.Careful(store.DefFlag(RedirectTLSKey, true))
	// срочность по RFC 8030: у тревоги high, у остального normal
	pushUrgencyAlertDef = store.DefText(pushUrgencyKey+"alert", "high")
	pushUrgencyWarnDef  = store.DefText(pushUrgencyKey+"warn", "normal")
	pushUrgencyInfoDef  = store.DefText(pushUrgencyKey+"info", "normal")

	// ---- Telegram: «Ответы и пределы» (telegram.go, tg_chat.go, tg_posts.go,
	// tg_wait.go, tg_talk.go) ----
	// 0 у «истории» законен — «не переносить»; остальное — целое больше нуля, а
	// ноль и мусор читаются как «не задан», как читали прежние Atoi
	tgReplyMaxHourDef = store.DefPositive("tg.reply_max_hour", 30, 1, 0)
	tgHistoryDef      = store.DefCount("tg.history", 30, 0, 0)
	tgFileLimitDef    = store.DefPositive("tg.file_limit_mb", 20, 1, 0)
	tgPostDaysDef     = store.DefPositive("tg.post_days", 30, 1, 0)
	tgChatsMaxDef     = store.DefPositive("tg.chats_max", 2000, 1, 0)
	tgWaitWarnDef     = store.DefPositive("tg.wait_warn_min", 30, 1, 0)
	tgWaitAlertDef    = store.DefPositive("tg.wait_alert_min", 120, 1, 0)
	tgWaitMsgsDef     = store.DefPositive("tg.wait_msgs", 3, 1, 0)
	// слово-вызов читается обрезанным, пустое — умолчанием (TGHelpWord)
	tgHelpWordDef = store.DefText("tg.help_word", "сёма")
	// не чаще раза в сто миллисекунд: ниже Telegram просит подождать
	tgStreamMsDef    = store.DefPositive("tg.stream_ms", 1200, 100, 0)
	tgStreamFirstDef = store.DefPositive("tg.stream_first", 16, 1, 0)

	// ---- коды на картинках (codes.go): «Тонкая настройка» «Инструментов» ----
	codesMaxDef = store.DefPositive(codesMaxKey, 8, 1, 600)
	codesMpDef  = store.DefPositive(codesMpKey, 40, 1, 400)

	// ---- «Работа Сёмы»: экран и связь, чаты команд, ветки (live.go,
	// notifications.go, nudge.go, threads.go) ----
	// Границы — те, что у этих полей были на «Уведомлениях»: числа были там
	// шагами с пределами, а сохранение их не проверяло. Читатели читают как
	// прежние `intSetting` (ноль и мусор — «не задан»), кроме `chats.nudge_max`,
	// у которого ноль законен. Часовой пояс (`app.timezone`) в реестре нет:
	// его «умолчание» — пояс самой машины, а не число или слово, которое можно
	// вписать в описание; пояс выбирает `SetTimezone`, он же его проверяет
	liveWindowDef     = store.DefPositive(settingLiveMS, 300, 100, 2000)
	livePollDef       = store.DefPositive(settingPollS, 4, 2, 60)
	livePollWiredDef  = store.DefPositive(settingPollLiveS, 60, 10, 600)
	liveWriteDef      = store.DefPositive(settingWSWriteS, 10, 3, 60)
	livePerSessionDef = store.DefPositive(settingWSPerSession, 8, 1, 32)
	snapshotDaysDef   = store.DefPositive(settingSnapshotDays, 7, 1, 90)
	commandTTLDef     = store.DefPositive(commandTTLKey, 24, 1, 720)
	nudgeAfterDef     = store.DefPositive(nudgeAfterKey, 5, 1, 120)
	nudgeMaxDef       = store.DefCount(nudgeMaxKey, 3, 0, 20)
	threadTailDef     = store.DefPositive(threadTailKey, 12, 1, 60)
	// шаги читающих вызовов и обещание без действия (chat_steps.go)
	chatReadStepsDef      = store.DefPositive(chatReadStepsKey, 4, 2, 10)
	chatPromiseMarkersDef = store.DefText(chatPromiseMarkersKey, promiseMarkersDefault)
	chatClaimMarkersDef   = store.DefText(chatClaimMarkersKey, claimMarkersDefault)
	// ссылки на файлы (share.go)
	shareDaysDef = store.DefPositive(shareDaysKey, 30, 1, 365)
	// поручения (assign.go): запуск, пределы, трекер
	assignConfirmDef       = store.DefFlag(assignConfirmKey, false)
	assignMaxStepsDef      = store.DefPositive(assignMaxStepsKey, 12, 2, 30)
	assignStepTurnsDef     = store.DefPositive(assignStepTurnsKey, 3, 1, 10)
	assignDaysDef          = store.DefPositive(assignDaysKey, 7, 1, 90)
	assignTrackerDef       = store.DefText(assignTrackerKey, assignTrackerAuto)
	assignTrackerProjDef   = store.DefText(assignTrackerProjKey, "")
	assignTrackerCreateDef = store.DefText(assignTrackerCreateKey, "task_create")
	assignTrackerDoneDef   = store.DefText(assignTrackerDoneKey, "task_complete")
	assignTrackerCancelDef = store.DefText(assignTrackerCancelKey, "task_cancel")

	// ---- скиллы, развитие, агенты, журнал настроек ----
	skillsMaxTokensDef = store.DefPositive(skillsMaxTokensKey, skillsMaxDefault, 500, 0)
	learnMaxDayDef     = store.DefPositive(learnMaxDayKey, 3, 1, 0)
	learnMinScoreDef   = store.DefPositive(learnMinScore, 5, 1, 0)
	// где слушать агентов и куда звонить шлюзу — открывают порт: осторожные (Careful)
	agentListenDef         = store.Careful(store.DefText(agentPortKey, ""))
	gatewayAddrDef         = store.Careful(store.DefText(gatewayAddrKey, ""))
	gatewayUpDef           = store.Careful(store.DefText(gatewayUpKey, ""))
	settingsLogKeepDaysDef = store.DefPositive(SettingsLogKeepKey, settingsLogKeepDef, 1, 0)
)

// DebugPprofKey — включён ли `/debug/pprof` (только с правом «всё»).
const DebugPprofKey = "debug.pprof"

// PprofOn — профилировщик включён: маршрут отвечает, на «Здоровье» строка.
func (e *Engine) PprofOn() bool { return e.store.SettingFlag(debugPprofDef) }

// ListRowsKey — сколько строк длинного списка показывать до «Показать ещё».
const ListRowsKey = "ui.list_rows"

// ListRows — первая порция длинного списка: строки расхода на «Здоровье»,
// «Мои задачи».
func (e *Engine) ListRows() int { return e.store.SettingInt(listRowsDef) }

// SlowSQLKey — порог медленного запроса к базе, мс; 0 — журнал не пишется.
const SlowSQLKey = "debug.slow_sql_ms"

// SlowSQLMs — порог медленного запроса, мс.
func (e *Engine) SlowSQLMs() int { return e.store.SettingInt(slowSQLDef) }

// PrefetchMsKey — срок жизни заранее подгруженного экрана, мс; 0 — выключено.
const PrefetchMsKey = "ui.prefetch_ms"

// PrefetchMs — сколько клиент держит экран, подгруженный при наведении.
func (e *Engine) PrefetchMs() int { return e.store.SettingInt(prefetchMsDef) }

// RedirectTLSKey — вести ли страницы с открытого порта на защищённый.
const RedirectTLSKey = "http.redirect_tls"

// RedirectTLS — открытый порт переадресует страницы на защищённый.
func (e *Engine) RedirectTLS() bool { return e.store.SettingFlag(redirectTLSDef) }
