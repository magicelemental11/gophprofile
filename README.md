# GophProfile

Сервис аватарок пользователей: загрузка, хранение, обработка и раздача изображений через REST API и веб-интерфейс. Оригинал сохраняется сразу, миниатюры 100×100 и 300×300 строит фоновый воркер, удалённые аватары убираются из хранилища асинхронно.

## Архитектура

```
                   ┌──────────────────────── server ────────────────────────┐
  клиент ──HTTP──▶ │ handlers (chi) ──▶ service ──▶ repository ──▶ PostgreSQL│
  браузер          │                       │   └──▶ storage ─────▶ MinIO (S3)│
                   │                       └──▶ broker ──┐                  │
                   └─────────────────────────────────────┼──────────────────┘
                                                         ▼
                                          RabbitMQ  avatars.exchange (topic)
                                            avatar.uploaded ─┐   avatar.deleted
                                            avatar.process  ─┤          │
                                                             ▼          ▼
                                            avatars.processing   avatars.deletion
                                                             │          │
                   ┌──────────────────────── worker ─────────┼──────────┼───────┐
                   │ worker ──▶ service.Processor ──▶ PostgreSQL, MinIO        │
                   └─────────────────────────────────────────────────────────────┘
                     сообщения, не обработанные после всех попыток ──▶ avatars.dlx ──▶ avatars.dlq
```

Слои (`internal/`):

- `domain` — сущность аватара, статусы, размеры и форматы, события, ошибки. Без зависимостей.
- `service` — сценарии: загрузка, выдача (в том числе конвертация и ресайз на лету), удаление, обработка событий воркером. Работает через интерфейсы из `service/ports.go`.
- `handlers` — REST API, веб-интерфейс, `/health`, middleware безопасности.
- `repository/postgres`, `storage/s3`, `broker/rabbitmq` — адаптеры к PostgreSQL (pgx, миграции golang-migrate встроены в бинарник), MinIO (minio-go) и RabbitMQ (amqp091-go).
- `imaging` — определение формата по magic bytes, декодирование, ресайз, кодирование JPEG/PNG/WebP.
- `worker` — разбор сообщений, повторные попытки с экспоненциальным backoff, отправка в DLQ.

Поток событий:

1. `POST /api/v1/avatars` проверяет файл, создаёт запись (`upload_status=uploading`), кладёт оригинал в `originals/{id}.{ext}`, отмечает `uploaded` и публикует `avatar.uploaded`. Клиент сразу получает 201 со статусом `processing`.
2. Воркер берёт сообщение из `avatars.processing`, строит миниатюры `thumbnails/{id}/{size}.jpg`, в одной транзакции сохраняет ключи, ставит `processing_status=completed` и записывает id сообщения в `processed_messages` — повторная доставка того же сообщения ничего не делает.
3. Ошибка обработки повторяется до `WORKER_MAX_RETRIES` раз с задержкой 0.5 с, 1 с, 2 с… (не больше 30 с). После последней попытки статус становится `failed`, сообщение уходит в `avatars.dlq`. Битое сообщение отправляется в DLQ сразу.
4. `DELETE` помечает запись удалённой (`deleted_at`) и публикует `avatar.deleted` с ключами файлов; воркер удаляет объекты из MinIO. Если аватар удалили во время обработки, воркер убирает уже созданные миниатюры.

Пока миниатюр нет, `?size=100x100` отдаётся ресайзом оригинала на лету.

## Запуск в Docker

```sh
cp .env.example .env
docker compose up --build -d   # или make up
```

Поднимаются server (`http://localhost:8080`), worker, PostgreSQL, RabbitMQ (management — `http://localhost:15672`) и MinIO (консоль — `http://localhost:9001`). Server и worker собираются в один образ из `docker/Dockerfile`. При потере соединения с RabbitMQ оба процесса завершаются, и Compose перезапускает их. Логи — `make logs`, остановка — `make down`.

MinIO запускается из образа `pgsty/minio` — официальные образы MinIO больше не публикуются.

## Запуск для разработки

```sh
cp .env.example .env
make up-deps       # PostgreSQL, RabbitMQ, MinIO
make run-server    # миграции применяются при старте
make run-worker
```

## Настройки

Каждый параметр задаётся флагом или переменной окружения; флаг приоритетнее.

| Переменная | Флаг | По умолчанию | Кто читает |
|---|---|---|---|
| `DATABASE_DSN` | `-d` | обязательна | server, worker |
| `AMQP_URL` | `-amqp` | обязательна | server, worker |
| `S3_ENDPOINT` | `-s3-endpoint` | обязательна (`host:port`) | server, worker |
| `S3_ACCESS_KEY` | `-s3-access-key` | обязательна | server, worker |
| `S3_SECRET_KEY` | `-s3-secret-key` | обязательна | server, worker |
| `S3_BUCKET` | `-s3-bucket` | `avatars` | server, worker |
| `S3_USE_SSL` | `-s3-use-ssl` | `false` | server, worker |
| `LOG_LEVEL` | `-log-level` | `info` | server, worker |
| `ADDRESS` | `-a` | `:8080` | server |
| `MAX_UPLOAD_SIZE` | `-max-upload-size` | `10485760` (10 МБ) | server |
| `WEB_DIR` | `-web-dir` | `./web` | server |
| `CORS_ALLOWED_ORIGINS` | `-cors-allowed-origins` | пусто | server |
| `RATE_LIMIT_RPS` | `-rate-limit-rps` | `100` | server |
| `UPLOAD_RATE_LIMIT` | `-upload-rate-limit` | `10` | server |
| `WORKER_PREFETCH` | `-prefetch` | `10` | worker |
| `WORKER_MAX_RETRIES` | `-max-retries` | `5` | worker |

## API

Ошибки возвращаются как `{"error": "...", "details": "..."}`. Поддерживаемые форматы — JPEG, PNG, WebP; размеры — `original`, `100x100`, `300x300`.

Загрузка (multipart, поле `file`, до 10 МБ):

```sh
curl -i -F file=@cat.png -H 'X-User-ID: gopher@example.com' http://localhost:8080/api/v1/avatars
# 201 {"id":"…","user_id":"gopher@example.com","url":"/api/v1/avatars/…","status":"processing","created_at":"…"}
# 400 {"error":"Invalid file format","details":"Supported formats: jpeg, png, webp"}
# 413 {"error":"File too large","max_size":10485760}
```

Картинка по id, с выбором размера и формата (по умолчанию — оригинал в исходном формате; ответы с `ETag`, поддерживаются `If-None-Match` → 304 без чтения файла и `HEAD`):

```sh
curl -o avatar.png http://localhost:8080/api/v1/avatars/{id}
curl -o thumb.webp 'http://localhost:8080/api/v1/avatars/{id}?size=100x100&format=webp'
```

Текущий (последний загруженный) аватар пользователя; если его нет — PNG-заглушка:

```sh
curl -o me.jpg 'http://localhost:8080/api/v1/users/gopher@example.com/avatar?size=300x300'
```

Метаданные и список аватаров пользователя (новые первыми):

```sh
curl http://localhost:8080/api/v1/avatars/{id}/metadata
# {"id":"…","user_id":"…","file_name":"cat.png","mime_type":"image/png","size":1024,
#  "dimensions":{"width":640,"height":480},
#  "thumbnails":[{"size":"100x100","url":"/api/v1/avatars/…?size=100x100"}, …],
#  "status":"ready","url":"/api/v1/avatars/…","created_at":"…","updated_at":"…"}
curl http://localhost:8080/api/v1/users/gopher@example.com/avatars
```

`status`: `processing` — миниатюры ещё строятся, `ready` — готовы, `failed` — не удалось загрузить или обработать.

Удаление — только своих аватаров (`X-User-ID` должен совпадать с владельцем):

```sh
curl -i -X DELETE -H 'X-User-ID: gopher@example.com' http://localhost:8080/api/v1/avatars/{id}
curl -i -X DELETE -H 'X-User-ID: gopher@example.com' http://localhost:8080/api/v1/users/gopher@example.com/avatar
# 204; чужой — 403 {"error":"Forbidden","details":"You can only delete your own avatars"}; нет такого — 404
```

Состояние зависимостей (200 или 503 `degraded`; подробности ошибки — в логе сервера):

```sh
curl http://localhost:8080/health
# {"status":"ok","components":{"broker":"ok","database":"ok","storage":"ok"}}
```

## Веб-интерфейс

`http://localhost:8080/` открывает форму загрузки, галерея пользователя — `/web/gallery/{user_id}`: миниатюры, статус обработки (обновляется раз в 2 секунды) и удаление. Страница основана на шаблоне курса [go-avatar-service-template](https://github.com/Yandex-Practicum/go-avatar-service-template) (MIT), стили Tailwind подключаются с CDN. Без JavaScript форма отправляется на `POST /web/upload` и после загрузки перенаправляет в галерею.

## Безопасность

- Формат определяется по magic bytes (JPEG, PNG, WebP), расширение и `Content-Type` клиента не учитываются. Перед декодированием проверяются размеры — больше 8000×8000 отклоняется (защита от decompression bomb), затем файл декодируется целиком: обрезанные файлы и данные после конца PNG или WebP (полиглоты) дают 400.
- Тело запроса ограничено через `MaxBytesReader` на обоих путях загрузки (`POST /api/v1/avatars` и `POST /web/upload`): `MAX_UPLOAD_SIZE` плюс 1 МБ на служебные части multipart, превышение — 413.
- Rate limiting по IP клиента (`RemoteAddr`): общий лимит на `/api/*` — `RATE_LIMIT_RPS` запросов в секунду, отдельный на загрузку через API и веб вместе — `UPLOAD_RATE_LIMIT` в минуту. Ответ 429 `{"error":"Too many requests"}` с `Retry-After`. Значение `0` отключает лимит. За обратным прокси все клиенты попадут в одну корзину — тогда лимиты нужно ставить на прокси.
- CORS: `CORS_ALLOWED_ORIGINS` — список origin через запятую (`*` — любой), им разрешены `GET, POST, DELETE` с заголовками `Content-Type, X-User-ID`. Картинки (`GET /api/v1/avatars/{id}`, `GET /api/v1/users/{user_id}/avatar`) доступны с любого origin, чтобы их можно было встраивать на сторонние сайты.
- `X-User-ID` и `{user_id}` в пути: непустая строка до 255 символов из `A-Z a-z 0-9 . _ % + @ -`, иначе 400 `{"error":"Invalid user id"}`.
- Все ответы идут с `X-Content-Type-Options: nosniff`, картинки — с `Content-Disposition: inline`. Имя файла из формы очищается от путей и управляющих символов и обрезается до 255 символов перед записью в БД. Внутренние ошибки возвращаются клиенту как `{"error":"Internal server error"}` без подробностей.

## Тесты и линтер

```sh
make test               # unit-тесты с -race
make cover              # покрытие unit-тестами (без cmd/)
make test-integration   # unit + интеграционные; нужен Docker, PostgreSQL, MinIO и RabbitMQ поднимаются через testcontainers
make lint               # golangci-lint v2
```

Интеграционные тесты помечены build-тегом `integration`. В CI (`.github/workflows/test.yml`) линтер, unit-тесты с покрытием и интеграционные тесты идут отдельными job.
