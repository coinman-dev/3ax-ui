# 3AX-UI — Project Guide for AI Assistants

> **Перед началом работы:** прочти этот файл полностью. Если что-то неясно или информация кажется устаревшей — уточни у пользователя перед тем как делать изменения.

---

## Что это за проект

**3AX-UI** — форк панели управления [3x-ui](https://github.com/mhsanaei/3x-ui), переработанный и переименованный с потдержкой AmneziaWG.  
Веб-панель для управления прокси-сервером **Xray-core** (vless, vmess, trojan, shadowsocks, wireguard/AWG).

- Репозиторий: `https://github.com/coinman-dev/3ax-ui`
- Go module: `github.com/coinman-dev/3ax-ui/v2`
- Язык бэкенда: Go 1.26
- Фронтенд: Vue 2 + Ant Design Vue (без сборщика, plain JS)
- База данных: SQLite (через GORM)
- Порт панели по умолчанию: **2053**
- Порт сервиса подписок по умолчанию: **2096**

---

## Структура каталогов

```
├── main.go                  — точка входа, CLI-команды (run, setting, ...)
├── config/                  — версия (config/version), имя (config/name), GetVersion(), IsBeta()
├── database/                — GORM, модели (Inbound, ClientStats, ...)
├── web/                     — HTTP-сервер панели (Gin)
│   ├── controller/          — HTTP-хендлеры (inbound, server, setting, ...)
│   ├── service/             — бизнес-логика (setting.go — все настройки приложения)
│   ├── middleware/           — domainValidator, auth, ...
│   ├── html/                — Go-шаблоны (.html)
│   ├── assets/              — JS, CSS, статика
│   │   ├── css/custom.min.css  — единый минифицированный CSS (правим напрямую)
│   │   └── js/subscription.js  — JS страницы подписки
│   ├── translation/         — i18n TOML-файлы (translate.en_US.toml, translate.ru_RU.toml, ...)
│   └── locale/locale.go     — загрузка переводов
├── sub/                     — отдельный HTTP-сервер подписок (порт 2096)
│   ├── sub.go               — старт/стоп, роутер, dual-stack IPv4+IPv6
│   ├── subController.go     — хендлеры /sub/:id и /json/:id
│   └── subService.go        — генерация ссылок vless/vmess/trojan/ss, PageData
├── awg/                     — AmneziaWG (форк WireGuard)
├── xray/                    — управление процессом Xray-core
├── logger/                  — логирование
├── util/                    — утилиты (common, random, ...)
├── .github/workflows/       — CI/CD
│   ├── release.yml          — основной: lint + build (7 платформ Linux + Windows)
│   ├── docker.yml           — Docker образ
│   └── cleanup_caches.yml   — очистка кешей CI
├── Makefile                 — локальная сборка
├── target/                  — бинарник после сборки (в .gitignore)
└── tmp/                     — временные файлы (в .gitignore)
```

---

## Версионирование

### Как работает версия

Версия задаётся **в момент сборки** через ldflags:

```bash
go build -ldflags "-X 'github.com/coinman-dev/3ax-ui/v2/config.version=v1.0.1-beta'" .
```

- При локальной разработке (`go run`) — читается из файла `config/version`
- При сборке через `make build` — берётся из `git describe --tags`
- При сборке в CI — берётся из имени тега (`github.ref_name`)

### Функции в config/config.go

```go
config.GetVersion()  // → "v1.0.1-beta" или "v1.0.2"
config.IsBeta()      // → true если содержит "beta", "alpha", "rc"
```

### Файл config/version

Содержит версию для `go run` / локальной разработки.  
**Обновлять вручную** при начале работы над новой версией.  
Текущее значение: `1.0.1-beta`

---

## Ветки и теги

### Ветки

| Ветка | Назначение |
|-------|-----------|
| `main` | Стабильные релизы. Только merge из `dev` через PR или напрямую когда готово |
| `dev` | Текущая разработка, beta-версии. Сюда идут все изменения |

**Правило:** новые фичи и фиксы — в `dev`. В `main` попадает только то, что прошло тестирование.

### Теги (семантическое версионирование)

```
v1.0.0          — стабильный релиз
v1.0.1-beta     — pre-release (beta)
v1.0.1-rc1      — release candidate
v1.1.0          — новые фичи
v2.0.0          — breaking changes
```

Теги `v2.8.x` — наследие от форка оригинального 3x-ui, их нет.  
Наши теги начинаются с `v1.0.0`.

---

## Как создавать релизы

### Beta / Pre-release

```bash
# Убедись что ты в ветке dev
git checkout dev

# Обнови config/version
echo -n "1.0.2-beta" > config/version
git add config/version && git commit -m "chore: bump version to 1.0.2-beta"
git push origin dev

# Создай тег
git tag v1.0.2-beta
git push origin v1.0.2-beta
```

CI автоматически:
1. Запускает lint + go vet + staticcheck + тесты
2. Собирает бинарники для 7 платформ Linux + Windows с `-X config.version=v1.0.2-beta`
3. Создаёт **pre-release** на GitHub (т.к. тег содержит `-`)

### Стабильный релиз

```bash
# Переключись на main и смержи dev
git checkout main
git merge dev

# Обнови config/version
echo -n "1.0.2" > config/version
git add config/version && git commit -m "chore: bump version to 1.0.2"
git push origin main

# Создай тег
git tag v1.0.2
git push origin v1.0.2
```

CI автоматически создаёт **обычный release** (тег без `-`).

### Логика prerelease в CI

```yaml
prerelease: ${{ contains(github.ref_name, '-') }}
# v1.0.2-beta → prerelease: true
# v1.0.2      → prerelease: false
```

---

## Локальная сборка

```bash
make build    # собирает target/3ax-ui с версией из git describe
make run      # build + запуск
make clean    # удаляет target/ и tmp/
```

Версия в собранном бинарнике:
- На теге `v1.0.1-beta` → `v1.0.1-beta`
- После тега с коммитами → `v1.0.1-beta-3-gabcdef`
- С незакоммиченными изменениями → `v1.0.1-beta-dirty`

---

## i18n (переводы)

Файлы: `web/translation/translate.{lang}.toml`

Основные языки: `en_US`, `ru_RU` (и ещё 11 языков).

Структура ключей — TOML-секции:
```toml
[subscription]
"title" = "Subscription info"
"pageThemeDesc" = "Default theme for the subscription info page."

[menu]
"dark" = "Dark"
"theme" = "Theme"

[pages.settings]
"subShowInfo" = "Show Usage Info"
```

**Правило:** при добавлении нового i18n ключа — добавить минимум в `en_US.toml` и `ru_RU.toml`.

---

## Темы (dark/light)

- Тема хранится в `localStorage['dark-mode']` браузера
- По умолчанию: **тёмная** (если localStorage пуст)
- При первом посещении страницы подписки — тема берётся из настройки `subTheme` (задаётся в панели)
- CSS: `body.dark { color-scheme: dark }` — для корректной работы с Dark Reader
- Переключатель: `web/html/component/aThemeSwitch.html`

---

## CI/CD — GitHub Actions

### release.yml

Триггер: push в `main`, `dev`, или любой тег `v*`.

Шаги:
1. **analyze** — `gofmt`, `go vet`, `staticcheck`, `go test`
2. **build** (matrix: 7 платформ Linux) — кросс-компиляция через Bootlin musl toolchain
3. **build-windows** — сборка под Windows (CGO + MinGW)
4. Артефакты: `x-ui-linux-{arch}.tar.gz`, `x-ui-windows-amd64.zip`
5. При теге — загрузка на GitHub Release

### Платформы сборки

`amd64`, `arm64`, `armv7`, `armv6`, `armv5`, `386`, `s390x` (Linux)  
`amd64` (Windows)

---

## Конвенции коммитов

Проект использует **Conventional Commits**:

```
feat(scope): описание      — новая функциональность
fix(scope): описание       — исправление бага
chore: описание            — служебные изменения (версия, зависимости)
docs: описание             — документация
refactor(scope): описание  — рефакторинг без изменения поведения
```

Примеры из истории:
```
fix(ipv6): fix subscription service over IPv6
feat(awg): make client UUID read-only in UI
chore: bump version to 1.0.2-beta
```

---

## Что НЕ нужно делать

- **Не коммитить** `target/` и `tmp/` — они в `.gitignore`
- **Не обновлять** теги вида `v2.8.x` — это наследие форка, оставлено для истории
- **Не пушить** напрямую в `main` крупные изменения — сначала в `dev`
- **Не хардкодить** версию в Go-коде — только через `config/version` + ldflags
- **Не редактировать** минифицированные файлы (`custom.min.css`) через инструменты форматирования — это минифицированный файл, правки добавляются в конец или через точечный поиск
- **Не пропускать** добавление i18n ключей хотя бы в `en_US.toml`

---

## Вопросы перед началом работы

Если что-то неясно — уточни:

1. В какой ветке работаем (`main` или `dev`)?
2. Это новая фича или фикс?
3. Нужен ли новый релиз после изменений?
