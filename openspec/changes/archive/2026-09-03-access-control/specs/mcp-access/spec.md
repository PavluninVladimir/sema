## ADDED Requirements

### Requirement: MCP и хуки с токеном

Запросы к `/mcp` и `/api/memory/session_*` SHALL требовать токен с правом
«память» в заголовке `Authorization: Bearer`; примеры в `integrations/`
SHALL брать токен из переменной окружения `SEMA_TOKEN`.

#### Scenario: Хук без токена

- **WHEN** хук Claude Code зовёт Сёму без токена
- **THEN** получает 401 с подсказкой, где выдать токен
