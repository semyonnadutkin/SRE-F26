ARG KUBECTL_VERSION=v1.37.0
FROM registry.k8s.io/kubectl:${KUBECTL_VERSION} AS kubectl

FROM python:3.14-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

COPY --from=kubectl /bin/kubectl /usr/local/bin/kubectl
WORKDIR /app
COPY . /app/scripts/

CMD ["python", "scripts/setup_k8s.py"]
