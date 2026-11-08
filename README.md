# GophProfile

Сервис аватарок пользователей: загрузка, хранение, обработка и раздача изображений через REST API.

## Запуск для разработки

```sh
cp .env.example .env
make up            # PostgreSQL, RabbitMQ, MinIO
make run-server    # миграции применяются при старте
make run-worker
```

Тесты: `make test` (unit), `make test-integration` (нужен Docker, зависимости поднимаются через testcontainers), линтер — `make lint`.

MinIO запускается из образа `pgsty/minio` — официальные образы MinIO больше не публикуются.

## Веб-интерфейс

`http://localhost:8080/` открывает форму загрузки, галерея пользователя — `/web/gallery/{user_id}`. Страница основана на шаблоне курса [go-avatar-service-template](https://github.com/Yandex-Practicum/go-avatar-service-template) (MIT), стили Tailwind подключаются с CDN. Без JavaScript форма отправляется на `POST /web/upload` и после загрузки перенаправляет в галерею.
