FROM python:3.12-slim

WORKDIR /app
RUN pip install --no-cache-dir "httpx>=0.27" "starlette>=0.37" "uvicorn>=0.30"

COPY cpa_usage_portal ./cpa_usage_portal
COPY run_usage_portal.py ./run_usage_portal.py

CMD ["python", "run_usage_portal.py"]
