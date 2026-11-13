FROM python:3.14-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app
COPY main.py ./main.py
USER 65532:65532

ENTRYPOINT ["python", "main.py"]
CMD ["normal"]
