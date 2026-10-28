FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /app

# Dependencies first, so this layer is cached until requirements.txt changes.
COPY requirements.txt .
RUN pip install -r requirements.txt

# Run as an unprivileged user.
RUN useradd --create-home --uid 1000 argo \
    && chown -R argo:argo /app

COPY --chown=argo:argo . .

USER argo

CMD ["python", "main.py"]
