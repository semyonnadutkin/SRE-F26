# SRE-F26 Проект: FileServer

Небольшой файловый сервер с аутентификацией по пользовательскому имени и паролю с логикой на Go и пользовательским интерфейсом HTML/CSS/JavaScript. Пользователи могут просматривать, загружать, переименовывать и удалять свои файлы. Для хранения пользовательских данных, метаданных файлов, сессий и ограничений на запросы используется PostgreSQL. Файлы сохраняются в локальном S3 хранилище (ограничение: до 50 MiB на файл).

## Запуск

### Docker Compose

**Требования:** Docker Engine, Docker Compose v2+, Make, Python 3 _(для генерации `.env`)_, Go 1.26+ _(для тестов)_.

```bash
# Генерируем .env со случайными данными (выходит, если .env уже существует)
make setup

# Собираем и запускаем в Docker Compose
make up
```

### Kubernetes

**Требования:** Minikube 1.39.0+, Docker, Make, Python3 + зависимости из `scripts/requirements.txt`.

**Установка Python-зависимостей в `.venv`:**
```
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install -r scripts/requirements.txt
```

**Запуск:**
```bash
# Запускаем кластер
# Пробрасываем порты:
#   - Grafana:      3000:30000
#   - VictoriaLogs: 9428:30001 
minikube start --driver=docker      \
    --ports=127.0.0.1:3000:30000    \
    --ports=127.0.0.1:9428:30001

# Добавляем Ingress
minikube addons enable ingress

# Собираем и загружаем изображение
docker build -t sre-f26-fileserver:local .
minikube image load sre-f26-fileserver:local

# Генерируем секреты и применяем манифесты
make k8s-apply
```
