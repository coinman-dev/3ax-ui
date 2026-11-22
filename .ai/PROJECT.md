# 3AX-UI — руководство для AI-агентов

> **Перед началом работы:** прочти этот файл, затем `AGENTS.md`, затем руководства upstream — `CLAUDE.md`, `frontend/CLAUDE.md`, по мере надобности `docs/architecture.md`. Если что-то неясно или кажется устаревшим — уточни у владельца, прежде чем менять.

---

## Что это за проект

**3AX-UI** — форк [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui). 2026-11-19 форк пересобран на свежем upstream `main` (v3.11.0 + исправления): код upstream взят целиком, а уникальное для 3AX-UI переносится поверх по плану `.ai/UPSTREAM_REBASE_PLAN.md` (локальный файл, в git не попадает).

- Репозиторий: `https://github.com/coinman-dev/3ax-ui`; remote `upstream` → MHSanaei/3x-ui.
- Архитектура, стек, правила тестов и кода — как у upstream: Go 1.27 + Gin + GORM в `internal/`, фронтенд React 19 + Ant Design 6 + Vite + TypeScript в `frontend/`, SQLite или PostgreSQL. Подробно — в `CLAUDE.md`.
- Старый код 3AX-UI 1.8.1 (Vue 2, `web/`, `tunnel/`, `nginx/` в корне) лежит в ветке `backup/dev-before-upstream-sync`. Оттуда берём логику при переносе, структуру — из upstream.

### Что уникально для 3AX-UI

Переносится по этапам плана; пока перенесено только то, что отмечено.

- [x] Идентичность: имя 3AX-UI в интерфейсе, иконки, обновления и скрипты смотрят на `coinman-dev/3ax-ui`, нет страницы Sponsors.
- [ ] Маскировка nginx: всё за портом 443, сайт-заглушка.
- [ ] Режим ядра AmneziaWG: публичный IPv6 клиентам через NDP proxy, проброс портов, маршрут через Xray (TPROXY).
- [ ] Пользователи с квотами у SOCKS5/HTTP (mixed/http).
- [ ] Доработки install/update/x-ui.sh, размер QR, карточки дашборда, Telegram-бот.
- [ ] Миграция с 3AX-UI 1.8.x.

---

## Правила, которые легко нарушить

### Держать разницу с upstream маленькой

Upstream делает сотни коммитов в месяц, и мы регулярно вливаем `upstream/main` в `dev`. Каждая наша правка в их файле — будущий конфликт.

- Свой код — в своих файлах и пакетах (`internal/web/service/fork_version.go`, `upstream_refs_test.go` и т. п.). В файлы upstream — только короткие точки подключения.
- **Путь Go-модуля не трогаем:** `github.com/mhsanaei/3x-ui/v3`. Пользователю он не виден, а переименование ломает каждый merge.
- **Имена на сервере не трогаем:** бинарник и сервис `x-ui`, пути `/usr/local/x-ui`, `/etc/x-ui`, имена архивов `x-ui-linux-<arch>.tar.gz`. На их совпадении держится обновление старых установок через `x-ui update`.
- Внутренние идентификаторы upstream (cookie сессии `3x-ui`, метки владения `3x-ui-owned-amneziawg`, комментарии правил nftables, имена сертификатов нод) не переименовываем: это совместимость, а не бренд.

### Ссылки на репозиторий

`upstream_refs_test.go` падает, если код, скрипты или воркфлоу ссылаются на репозиторий MHSanaei (релизы, raw-файлы, API, образ). Иначе панель предложила бы обновиться до их 3x-ui. После каждого merge из upstream этот тест показывает, какие новые ссылки нужно перенаправить на `coinman-dev/3ax-ui`. Разрешены: путь модуля, ссылки на их issues/PR, `mhsanaei/mtg-multi` (панель использует API именно этой сборки).

### Версии

- Версия — в `internal/config/version` (без `v`), встраивается через `go:embed`. Тег релиза обязан совпадать с ней: `release.yml` проверяет `v$(cat internal/config/version) == тег`.
- Наша нумерация — **2.x** (первый релиз на новой основе — 2.0.0, выходит только после этапа тестирования и явного решения владельца). Тег с `-` — пререлиз.
- Upstream ограничивает протоколы на нодах по своим номерам версий (`nodeProtocolFirstRelease` в `internal/web/service/inbound_protocol.go`). Наши 2.x переводятся в номер upstream через `upstreamComparableVersion` (`internal/web/service/fork_version.go`). **После каждого merge из upstream, который войдёт в релиз, поднимай `axUpstreamBase`** до версии upstream, на которой стоит этот релиз.
- Если когда-нибудь 3AX-UI дойдёт до 3.x — номера пересекутся с upstream, и эту схему придётся менять.

### Ветки

| Ветка | Назначение |
|---|---|
| `dev` | вся разработка и merge из upstream |
| `main` | стабильные релизы; попадает туда через ready PR из `dev` |
| `backup/dev-before-upstream-sync` | 3AX-UI 1.8.1 до пересборки; источник кода для переноса, не удалять |

---

## Сборка и проверки локально

Нужны Go 1.27, Node 26 (`.nvmrc`), C-компилятор (CGo SQLite), golangci-lint v2.15.0, shellcheck. Для фронтенд-тестов: `npx playwright install chromium` в `frontend/`.

```bash
make dist-stub              # один раз в свежем клоне, иначе go build падает на go:embed
make test-go                # Go-тесты
cd frontend && npm ci && npm test
make verify                 # полный локальный гейт: gen-check, lint, format, typecheck, тесты, сборка, storybook
make race                   # как в CI
```

`make verify` проверяет и `docs/public/openapi.json`: после изменения API копируй `frontend/public/openapi.json` туда. MDX сайта документации не перегенерируем (сайт не публикуем).

---

## CI/CD

Политика экономии минут — в `AGENTS.md`, она главнее описаний в `CLAUDE.md`. Кратко:

- push в `main` → только `golangci` и `shell-lint`;
- ready PR / ручной запуск → полный `ci.yml`;
- тег `v*.*.*` → `release.yml`: сначала полный `ci.yml` как гейт, потом 7 Linux-архитектур + Windows, публикация в Releases; Docker-образ не собирается (решение владельца 2026-11-22, `docker.yml` удалён — при слиянии с upstream не возвращать);
- CodeQL — ready PR, вручную, раз в неделю.

Проверка правок воркфлоу — `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck=`, без пробных тегов и запусков.

---

## Конвенции

Как у upstream (`CLAUDE.md`): Conventional Commits `type(area): summary` с телом про «почему»; тесты пишутся до кода и должны уметь падать; комментарии не длиннее двух строк; новый endpoint — в `frontend/src/pages/api-docs/endpoints.ts` + `make gen`; новый ключ перевода — во все 14 JSON в `internal/web/translation/` и сразу используется в коде.
