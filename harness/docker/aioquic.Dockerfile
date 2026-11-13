FROM python:3.12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 \
 && rm -rf /var/lib/apt/lists/* \
 && pip install --no-cache-dir aioquic==1.8.0
COPY servers/aioquic/server.py /opt/qth/server.py
