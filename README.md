# synk — сервер синхронизации Money Tracks

> **English.** synk is the self-hosted sync server for the Money Tracks app
> (iPhone, Android, Mac). Your data stays on your own server; the developer
> runs no server and sees nothing. One static Go binary, pure-Go SQLite, no
> cgo. Quick start: `docker compose up -d`, put it behind nginx with TLS,
> run `synk account add --bootstrap` and enter the printed code in the app.
> Licensed under AGPL-3.0.

Сервер синхронизации для приложения Money Tracks: устройства одной учётки
(iPhone, Android, Mac) обмениваются правками через **ваш** сервер. У
разработчика приложения своего сервера нет, адреса по умолчанию в
приложении нет — сервер поднимаете вы.

Сервер — тупой упорядоченный лог правок плюс материализованное состояние:
принять правку, присвоить `seq`, отдать правки после курсора. Бизнес-логики
нет, данные — те же строки, что в базе приложения. Go, чистый SQLite
(`modernc.org/sqlite`, без cgo), один бинарь.

## Что понадобится

- Linux-сервер (подойдёт самый маленький VPS: сотни КБ на правку, чеки — сотни КБ);
- домен и TLS-сертификат (Let's Encrypt): токен устройства ходит заголовком,
  поэтому только https;
- nginx (или любой обратный прокси) перед сервером.

## Вариант 1. Docker

```bash
git clone https://github.com/hobby-me/money-tracks-sync.git
cd money-tracks-sync
# в docker-compose.yml замените SYNK_PUBLIC_URL на свой адрес
docker compose up -d
curl -s http://127.0.0.1:8090/healthz   # ok
```

Данные — в томе `synk-data`. Процесс в контейнере не root, образ без оболочки.

## Вариант 2. systemd

```bash
make build-linux                     # bin/synk-linux-amd64
sudo useradd --system --shell /usr/sbin/nologin synk
sudo install -d -o synk -g synk -m 0700 /opt/synk/data
sudo install -m 0755 bin/synk-linux-amd64 /opt/synk/synk
sudo install -m 0755 deploy/examples/synk-tui /opt/synk/synk-tui
sudo install -m 0644 deploy/examples/synk.env.example /opt/synk/synk.env   # поправьте адрес
sudo install -m 0644 deploy/examples/synk.service /etc/systemd/system/synk.service
sudo systemctl daemon-reload && sudo systemctl enable --now synk
```

Юнит изолирует процесс: пишет только в `/opt/synk/data`, файлы создаются
`0600`, сеть — только localhost, без capabilities. Подробности —
[docs/security.md](docs/security.md).

## nginx и TLS

Пример — [deploy/examples/nginx-synk.conf](deploy/examples/nginx-synk.conf):
замените `sync.example.com` на свой домен и выпустите сертификат
`certbot --nginx -d sync.example.com`. Метрики (`127.0.0.1:8091`) наружу не
проксируются намеренно.

## Первая учётка и первое устройство

```bash
# systemd:
sudo -u synk /opt/synk/synk account add --data /opt/synk/data --name <имя> --bootstrap
# Docker:
docker compose run --rm synk account add --data /data --name <имя> --bootstrap
```

Команда печатает блок подключения: адрес сервера и одноразовый **код
администратора** (действует 10 минут). В приложении: Настройки →
«Синхронизация устройств» → «Подключиться к серверу», адрес и код.

- Если в приложении уже есть данные, а сервер пуст — данные уходят на сервер.
- Если на сервере уже есть данные, база устройства заменяется ими, а
  прежняя сохраняется копией: две истории никогда не смешиваются.

Остальные устройства подключаются кодами, которые выпускает
админ-устройство («Подключить устройство») или консоль оператора.

## Обслуживание

- **Консоль оператора** — `/opt/synk/synk-tui` (или `synk --data DIR --tui`),
  интерфейс в духе k9s: учётки, устройства, коды, квоты, дашборд диска и
  нагрузки. Подробно — [docs/tui.md](docs/tui.md).
- **CLI:** `synk invite --account <id|имя>`, `synk invite list|cancel|purge`,
  `synk device list|revoke|demote`, `synk status`, `synk account delete`,
  `synk account quota --account <id|имя> --mb N`, `synk trash empty`.
- **Квота:** по умолчанию `SYNK_ACCOUNT_QUOTA_MB` (в примере 1 ГБ), у учётки
  может быть своя. Переполнение приложение показывает и ждёт, данные не теряются.
- **Резервная копия:** остановить сервис и скопировать папку данных —
  одна SQLite на учётку, фото рядом.

## Структура данных

```
data/
  accounts.db          — учётки, устройства (токены — SHA-256-хэшами), инвайты
  acc/<account_id>/
    sync.db            — ops (лог) + rows_state (LWW-состояние) + instance_id
    photos/<ULID>.jpg  — чеки
```

## Разработка

```bash
make test          # go vet + go test
make build         # bin/synk под текущую платформу (CGO_ENABLED=0)
make run-tui       # консоль на временных данных с примером учётки
make docker        # образ synk:<версия>
```

Слои: `cmd/synk` (CLI), `internal/tui` (консоль), `internal/admin` (все
операции оператора), `internal/server` (HTTP), `internal/store` (SQLite).
Новая операция добавляется в `internal/admin` один раз и выводится и в CLI,
и в консоли.

## Протокол

`Authorization: Bearer <токен>`, JSON. Ручки — в `internal/server/server.go`:
регистрация по коду, идемпотентный push по `op_id`, pull после курсора
`seq`, снимок состояния, контрольные суммы, фото, квота. LWW по
`(hlc, device_id)`, `instance_id` в каждом ответе — по нему клиент узнаёт,
что сервер пересоздан.

## Лицензия

[AGPL-3.0](LICENSE). Свой сервер для себя, семьи или команды — без
ограничений. Если вы запускаете изменённый сервер как услугу для других,
откройте свои изменения.
