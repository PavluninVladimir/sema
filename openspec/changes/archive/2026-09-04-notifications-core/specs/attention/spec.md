## REMOVED Requirements

### Requirement: Модуль «Требует внимания»

**Reason**: напоминания, правила почты и блок на «Почте» переезжают в
ядро как уведомления (`notifications`); модуль сносится, его данные
переносятся миграцией.

**Migration**: коллекция `items` модуля → `notifications` (открытые и
сделанные), `rules` → справочник правил ядра, `attention.remind` →
`sema.remind` (старое имя — синоним до следующего изменения), экран
`module:attention:attention` → «Уведомления».
