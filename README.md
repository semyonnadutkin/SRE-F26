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

**Требования:** Minikube 1.39.0+, Docker, Make, Python3 + PyYAML

**Установка PyYAML в `.venv`:** 
```
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install scripts/requirements.txt
```

**Запуск:**
```bash
# Запускаем кластер
minikube start --driver=docker

# Добавляем Ingress
minikube addons enable ingress

# Собираем и загружаем изображение
docker build -t sre-f26-fileserver:local .
minikube image load sre-f26-fileserver:local

# Генерируем секреты и применяем манифесты
make k8s-apply
```
