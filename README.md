# GophProfile

Сервис аватарок пользователей: загрузка, хранение, обработка и раздача изображений через REST API.

## Запуск в Docker

```sh
cp .env.example .env
docker compose up --build -d   # или make up
```

Поднимаются server (`http://localhost:8080`), worker, PostgreSQL, RabbitMQ (management — `http://localhost:15672`) и MinIO (консоль — `http://localhost:9001`). Server и worker собираются в один образ из `docker/Dockerfile`. При потере соединения с RabbitMQ оба процесса завершаются, и Compose перезапускает их. Логи — `make logs`.

## Запуск для разработки

```sh
cp .env.example .env
make up-deps       # PostgreSQL, RabbitMQ, MinIO
make run-server    # миграции применяются при старте
make run-worker
```

Тесты: `make test` (unit), `make test-integration` (нужен Docker, зависимости поднимаются через testcontainers), линтер — `make lint`.

MinIO запускается из образа `pgsty/minio` — официальные образы MinIO больше не публикуются.

## Веб-интерфейс

`http://localhost:8080/` открывает форму загрузки, галерея пользователя — `/web/gallery/{user_id}`. Страница основана на шаблоне курса [go-avatar-service-template](https://github.com/Yandex-Practicum/go-avatar-service-template) (MIT), стили Tailwind подключаются с CDN. Без JavaScript форма отправляется на `POST /web/upload` и после загрузки перенаправляет в галерею.

## Безопасность

- Формат определяется по magic bytes (JPEG, PNG, WebP), расширение и `Content-Type` клиента не учитываются. Перед декодированием проверяются размеры — больше 8000×8000 отклоняется (защита от decompression bomb), затем файл декодируется целиком: обрезанные файлы и данные после конца PNG или WebP (полиглоты) дают 400.
- Тело запроса ограничено через `MaxBytesReader` на обоих путях загрузки (`POST /api/v1/avatars` и `POST /web/upload`): `MAX_UPLOAD_SIZE` плюс 1 МБ на служебные части multipart, превышение — 413.
- Rate limiting по IP клиента (`RemoteAddr`): общий лимит на `/api/*` — `RATE_LIMIT_RPS` запросов в секунду, отдельный на загрузку через API и веб вместе — `UPLOAD_RATE_LIMIT` в минуту. Ответ 429 `{"error":"Too many requests"}` с `Retry-After`. Значение `0` отключает лимит. За обратным прокси все клиенты попадут в одну корзину — тогда лимиты нужно ставить на прокси.
- CORS: `CORS_ALLOWED_ORIGINS` — список origin через запятую (`*` — любой), им разрешены `GET, POST, DELETE` с заголовками `Content-Type, X-User-ID`. Картинки (`GET /api/v1/avatars/{id}`, `GET /api/v1/users/{user_id}/avatar`) доступны с любого origin, чтобы их можно было встраивать на сторонние сайты.
- `X-User-ID` и `{user_id}` в пути: непустая строка до 255 символов из `A-Z a-z 0-9 . _ % + @ -`, иначе 400 `{"error":"Invalid user id"}`.
- Все ответы идут с `X-Content-Type-Options: nosniff`, картинки — с `Content-Disposition: inline`. Имя файла из формы очищается от путей и управляющих символов и обрезается до 255 символов перед записью в БД.

| Переменная | Флаг | По умолчанию |
|---|---|---|
| `CORS_ALLOWED_ORIGINS` | `-cors-allowed-origins` | пусто (только картинки с любого origin) |
| `RATE_LIMIT_RPS` | `-rate-limit-rps` | 100 |
| `UPLOAD_RATE_LIMIT` | `-upload-rate-limit` | 10 |
