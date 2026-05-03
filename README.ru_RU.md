[English](/README.md) | [Русский](/README.ru_RU.md)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./media/3ax-ui-dark.png">
    <img alt="3ax-ui" src="./media/3ax-ui-light.png">
  </picture>
</p>

[![Release](https://img.shields.io/github/v/release/coinman-dev/3ax-ui.svg)](https://github.com/coinman-dev/3ax-ui/releases)
[![Build](https://img.shields.io/github/actions/workflow/status/coinman-dev/3ax-ui/release.yml.svg)](https://github.com/coinman-dev/3ax-ui/actions)
[![GO Version](https://img.shields.io/github/go-mod/go-version/coinman-dev/3ax-ui.svg)](#)
[![Downloads](https://img.shields.io/github/downloads/coinman-dev/3ax-ui/total.svg)](https://github.com/coinman-dev/3ax-ui/releases/latest)
[![License](https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true)](https://www.gnu.org/licenses/gpl-3.0.en.html)

**3AX-UI** — форк панели управления [3x-ui](https://github.com/MHSanaei/3x-ui), расширенный встроенной поддержкой протокола **AmneziaWG**.

> **A** в названии означает **Amnezia** — протокол, который является основным отличием этой панели от оригинала.

> [!IMPORTANT]
> Проект предназначен для личного использования. Пожалуйста, не используйте его в незаконных целях.

## Быстрый старт

```bash
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh)
```

Для установки последней pre-release версии:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh) --beta
```
---

## Зачем эта панель?

Оригинальная 3x-ui построена вокруг ядра **Xray** и поддерживает протоколы VLESS, VMess, Trojan, Shadowsocks и WireGuard. Однако **AmneziaWG** — модифицированный WireGuard с обфускацией трафика — в оригинале не поддерживается.

**3AX-UI** решает эту задачу: AmneziaWG интегрирован напрямую в панель и управляется точно так же, как любой другой протокол — через привычный интерфейс подключений.

---

## Главные отличия от оригинальной 3x-ui

### 1. Полная поддержка AmneziaWG

AmneziaWG — это WireGuard с добавленной обфускацией пакетов. Обычный WireGuard легко детектируется и блокируется DPI-системами (Россия, Иран, Китай). AmneziaWG делает трафик неотличимым от случайного шума.

**Что добавлено:**
- Отдельная страница настроек AWG-сервера (сетевые параметры, пул адресов IPv4/IPv6, параметры обфускации)
- Управление клиентами AWG прямо со страницы **Подключения** — так же, как VLESS или Trojan
- Для каждого клиента: автоматическая генерация ключей (приватный, публичный, preshared), выделение IP из пула, QR-код, скачивание `.conf` файла
- Сбор статистики трафика каждые 10 секунд (upload/download на клиента)
- Лимиты трафика и дата окончания — всё то же, что и у других протоколов

### 2. Параметры обфускации AmneziaWG

На странице настроек AWG можно настроить параметры обфускации пакетов:

| Параметр | Описание |
|----------|----------|
| `Jc` | Количество junk-пакетов перед handshake |
| `Jmin` / `Jmax` | Минимальный и максимальный размер junk-пакетов |
| `S1` / `S2` | Размер заголовков init/response |
| `H1` – `H4` | Magic headers для разных типов пакетов |

Эти параметры автоматически прописываются в конфиг каждого клиента — пользователю ничего настраивать не нужно.

### 3. Поддержка IPv6 без NAT

Клиентам AWG может выдаваться **нативный публичный IPv6-адрес** сервера — без NAT66. Это работает через NDP proxy (ndppd или встроенный fallback через `ip -6 neigh add proxy`). Клиент получает реальный IPv6, что важно для сервисов, требующих его поддержки.

#### Если IPv6 не работает: ограничения на стороне провайдера

NDP proxy может не работать на VPS по причинам, не зависящим от настроек сервера:

**1. Гипервизор блокирует NDP-пакеты (MAC-фильтрация)**

Многие провайдеры на уровне гипервизора разрешают VPS отправлять пакеты только с MAC-адреса её сетевого интерфейса. Когда `ndppd` пересылает Neighbor Advertisement от имени клиента, гипервизор воспринимает это как IP-спуфинг и дропает пакет. Внутри VPS всё выглядит корректно, но IPv6-трафик клиентов до интернета не доходит.

**2. Провайдер выдаёт «link prefix», а не «routed prefix»**

NDP proxy работает только тогда, когда блок IPv6-адресов **маршрутизируется непосредственно на ваш VPS**. Многие провайдеры подключают несколько VPS к одной виртуальной сети и выдают адреса из общего пула — в таком случае NDP proxy на уровне VPS не поможет.

#### Что делать

Обратитесь в поддержку провайдера. Нужно выяснить:
- **Тип выделения IPv6:** маршрутизируемый /64-префикс (routed prefix) или адрес из общего пула (link prefix). Только routed prefix позволяет использовать NDP proxy.
- **NDP proxy на гипервизоре:** есть ли опция включения NDP proxy / Neighbor Discovery на уровне хоста.
- **Разрешение IP-спуфинга:** попросите разрешить пересылку NDP-пакетов с вашего VPS.

> **Формулировка для поддержки (на английском):**
> *"I'm running a server with multiple virtual network interfaces and need to assign individual public IPv6 addresses from my /64 block to each of them using NDP proxy. Could you please confirm whether my IPv6 allocation is a fully routed /64 prefix routed to my VM directly, and whether NDP Neighbor Advertisement packets originated from my VM are allowed through the hypervisor — or if they are dropped by MAC/ARP filtering on the host node?"*

### 4. Автоматическая установка AmneziaWG

Скрипт установки (`install.sh`) автоматически:
- Устанавливает ядро AmneziaWG через PPA `ppa:amnezia/ppa`
- Устанавливает `awg-tools` and `ndppd`
- Определяет внешний интерфейс сервера и настраивает PostUp/PostDown правила
- Настраивает автозапуск AWG после перезагрузки сервера
- Обнаруживает Secure Boot и предупреждает о возможных проблемах с DKMS-модулем

### 5. Настраиваемый размер QR-кодов

В настройках панели добавлена опция **Размер QR-кода**:
- 300×300 px — компактный
- 450×450 px — стандартный (по умолчанию)
- 600×600 px — крупный

### 6. Безопасный URL подписки по умолчанию

При установке панели URL-путь подписки автоматически генерируется со случайным 12-символьным суффиксом (например `/sub-Xk92mPqLvzRt/`) вместо стандартного `/sub/`. Это снижает риск случайного обнаружения.

### 7. Проброс портов на клиента в AmneziaWG / native WireGuard

Каждому пиру можно пробросить произвольные внешние порты прямо на его тоннельный IP — одновременно **по TCP и по UDP**. Делалось специально под игровые серверы, P2P, голосовые приложения, всё что хочет входящий порт.

**Формат ввода** (свободный, с валидацией):
- одиночные порты: `80, 443, 22`
- диапазоны через дефис: `8000-8100`
- любые комбинации, разделители `,` или `;`: `80, 443; 27015-27030`

**Как работает.** На каждого включённого клиента с непустыми пробросами панель добавляет в wg-quick `PostUp`/`PostDown` правила `iptables` DNAT и FORWARD (TCP и UDP). Изменения применяются **на лету** через `iptables -A`/`-D` без перезапуска тоннеля — сессии других клиентов не разрываются. У каждого правила уникальный комментарий `3ax-fwd-<uuid>`, так что удаление пробросов одного клиента не задевает другого.

Проброшенные порты видны в трёх местах:
- в форме редактирования клиента (с подсказкой формата),
- в отдельной колонке «Маппинг» в peer-таблице inbound'а,
- строкой в окне «Подробнее» сразу после «Порт».

### 8. SOCKS5 и HTTP-прокси с полной per-user инфраструктурой

xray-core inbound'ы `mixed` (SOCKS5) и `http` теперь используют **тот же VLESS-style стек**, что VLESS / VMess / Trojan / Shadowsocks:
- раскрывающаяся peer-таблица с per-client трафиком, экспайром, квотой, IP-лимитом, тумблером Включить;
- стандартное модальное окно редактирования клиента (автогенерация: 6-символьный логин + 16-символьный пароль, кнопка «regenerate»);
- per-user трафик идёт через стандартные xray-ключи `user>>>EMAIL>>>traffic>>>...`, так что существующие job'ы (учёт трафика и автоотключение по квоте/экспайру) работают для MIXED/HTTP без отдельного кода;
- пункт «Добавить клиента» в action-меню inbound'а — как у VLESS.

Логин остаётся редактируемым после создания — переименование клиента не сбрасывает счётчики трафика, бэкенд переименовывает `client_traffic`-строку in-place.

### 9. Установка / обновление из локального git-клона

Скрипты `install.sh` и `update.sh` теперь определяют, что их запускают из клонированного репозитория (наличие файлов + проверка `BASH_SOURCE`) и **собирают бинарь панели на месте из локальных исходников** вместо скачивания готового релиза с GitHub.

```bash
git clone https://github.com/coinman-dev/3ax-ui.git
cd 3ax-ui
sudo bash install.sh
```

Если на хосте нет Go ≥ 1.21, скрипт автоматически скачает Go 1.26.2 с go.dev. С Go ≥ 1.21 сборка сама докачает тулчейн, прописанный в `go.mod`. Удалённые pipe-флоу (`bash <(curl ...)`, `curl ... | bash`) сохраняют прежнее поведение — safety-check отвергает их, так что пользователь, оказавшийся внутри клона репозитория во время piping'а, не попадёт случайно на локальную сборку.

`x-ui.db` и `bin/` сохраняются между переустановками и обновлениями — повторный запуск установщика **не сносит** базу панели.

### 10. Режим отладки / диагностики

Первый вопрос при установке:

```
Install panel in debug / diagnostic mode (localhost only)? [y/N]
(HTTP only, listen=127.0.0.1, default port 8080, no SSL or IPv6)
```

При `y` панель биндится на `127.0.0.1`, поднимается по обычному HTTP на выбранном порту, пропускает SSL-prompt, public-IP detection и всю IPv6-логику. Включается также неинтерактивно через `XUI_DEBUG_MODE=1` (и опционально `XUI_DEBUG_PORT=NNNN`).

`update.sh` **не задаёт вопрос** — сам определяет, в каком режиме была существующая установка (по `listenIP == 127.0.0.1` и отсутствию SSL-сертификата) и продолжает с тем же портом. Обновления на debug-боксе проходят без интерактива.

Стеки VPN-протоколов (AmneziaWG, native WireGuard, xray) ставятся в debug-режиме как обычно — на loopback ограничен только web-доступ к самой панели.

---

## Требования к серверу

- **ОС:** Ubuntu 22.04+ / Debian 11+
- **Ядро Linux:** 5.6+ (для встроенного WireGuard), или установленный DKMS-модуль AmneziaWG
- **RAM:** от 1024 МБ
- **Архитектура:** amd64 / arm64

> **Secure Boot:** Если на сервере включён Secure Boot, DKMS-модуль AmneziaWG может не загрузиться. Скрипт установки предупредит об этом автоматически.

---

## Установка

```bash
# Стабильная версия
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh)

# Последняя pre-release версия
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh) --beta

# Конкретная версия
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/install.sh) v1.2.1
```

## Обновление панели

```bash
# Стабильная версия
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/update.sh)

# Последняя pre-release версия
bash <(curl -Ls https://raw.githubusercontent.com/coinman-dev/3ax-ui/main/update.sh) --beta
```

---

## Быстрый старт с AmneziaWG

1. Войдите в панель → **Настройки AWG**
2. Настройте сетевые параметры и параметры обфускации
3. Перейдите на страницу **Подключения** → **Создать подключение**
4. Выберите протокол **amneziawg** — введите Email клиента и нажмите **Создать**
5. В таблице клиентов нажмите на иконку QR-кода — отсканируйте в приложении AmneziaVPN

---

## Совместимые клиенты AmneziaWG

| Клиент | Платформа | Ссылка |
|--------|-----------|--------|
| AmneziaVPN | Android, iOS, Windows, macOS, Linux | [amnezia.org](https://amnezia.org) |

> Стандартные WireGuard-клиенты **не совместимы** с AmneziaWG — они не поддерживают параметры обфускации.

---

## Основа

3AX-UI основан на **[3x-ui](https://github.com/MHSanaei/3x-ui)** за авторством [MHSanaei](https://github.com/MHSanaei). Все оригинальные возможности (VLESS, VMess, Trojan, Shadowsocks, WireGuard, Xray, подписки, Telegram-бот и т.д.) полностью сохранены.

## Благодарности

- [MHSanaei](https://github.com/MHSanaei/) — автор оригинальной 3x-ui
- [alireza0](https://github.com/alireza0/) — автор оригинальной x-ui
- [Iran v2ray rules](https://github.com/chocolate4u/Iran-v2ray-rules) (GPL-3.0)
- [Russia v2ray rules](https://github.com/runetfreedom/russia-v2ray-rules-dat) (GPL-3.0)

---

## Лицензия

Проект распространяется под той же лицензией, что и оригинальная 3x-ui — [GNU GPL v3](LICENSE).
