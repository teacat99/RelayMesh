# ------------------------------------------------------------
# Minimal Production & Preview Image
# ------------------------------------------------------------
FROM alpine:3.21 AS runner
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S relaymesh && adduser -S relaymesh -G relaymesh \
    && mkdir -p /app/data && chown -R relaymesh:relaymesh /app

COPY bin/relaymesh /app/relaymesh

USER relaymesh

ENV RELAYMESH_HOST=0.0.0.0 \
    RELAYMESH_PORT=18775 \
    RELAYMESH_HTTPS_PORT=18776 \
    RELAYMESH_DB_PATH=/app/data/relaymesh.db \
    RELAYMESH_TLS_CERT=/app/data/certs/cert.pem \
    RELAYMESH_TLS_KEY=/app/data/certs/key.pem \
    GIN_MODE=release

EXPOSE 18775 18776

VOLUME ["/app/data"]

ENTRYPOINT ["/app/relaymesh"]
