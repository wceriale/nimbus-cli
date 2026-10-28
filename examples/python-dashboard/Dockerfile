FROM python:3.13-slim

# Nimbus runs the container with a read-only root filesystem, so don't write .pyc
# files, and flush logs straight to stdout.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir --root-user-action=ignore -r requirements.txt

# Copy only the app, so a local data/ or .venv/ never ends up in the image.
COPY app.py seed.py ./
COPY templates/ templates/

# One worker, because SQLite and Nimbus run one instance; threads handle concurrent
# requests. --access-logfile - sends access logs to stdout.
CMD exec gunicorn --bind "0.0.0.0:${PORT:-8080}" --workers 1 --threads 8 \
    --access-logfile - app:app
