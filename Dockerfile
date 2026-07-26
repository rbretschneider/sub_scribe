# syntax=docker/dockerfile:1

# --- Build stage: compile a fully static binary (pure-Go SQLite, no CGo) -------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache modules first so source edits don't re-download dependencies.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO disabled => a static binary that runs on the minimal runtime image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/subscribe ./cmd/subscribe

# --- Runtime stage: the app binary plus the external tools it shells out to ----
# Alpine 3.22 is the floor: it ships Node 22, and yt-dlp rejects the Node 20.15
# in older Alpines as an unsupported JS runtime ("JS runtimes: node-20.15.1
# (unsupported)"), which silently costs you formats on every YouTube download.
FROM alpine:3.22
# yt-dlp + ffmpeg do the actual media work; apprise (optional) powers
# notifications; ca-certificates lets yt-dlp reach YouTube over TLS. The
# bgutil-ytdlp-pot-provider plugin is the lightweight (Python) client half of the
# PO-token feature — it lets yt-dlp talk to an optional provider container set via
# SUBSCRIBE_POT_PROVIDER_URL. The provider server itself is a separate container
# (see docker-compose.yml), so nothing heavy is added here.
# yt-dlp comes from pip (not the Alpine package) so it stays current — YouTube
# changes constantly, and the bgutil PO-token plugin requires a recent yt-dlp.
# The system-wide yt-dlp config enables the JS runtime and the EJS challenge
# solver for every invocation, so the app never has to pass those flags itself:
#   --js-runtimes node       nodejs solves YouTube's player signatures
#   --remote-components      fetches yt-dlp's EJS solver script; without it
#     ejs:github             YouTube's "n challenge" fails and formats go missing
RUN apk add --no-cache ca-certificates ffmpeg nodejs python3 py3-pip \
    && pip install --no-cache-dir --break-system-packages yt-dlp apprise bgutil-ytdlp-pot-provider \
    && printf -- '--js-runtimes node\n--remote-components ejs:github\n' > /etc/yt-dlp.conf \
    && rm -rf /root/.cache

COPY --from=build /out/subscribe /usr/local/bin/subscribe

# Persistent config/db/cookies/feeds live here; media is written here.
VOLUME ["/config", "/media"]
ENV SUBSCRIBE_DATA_DIR=/config \
    SUBSCRIBE_MEDIA_DIR=/media \
    SUBSCRIBE_PORT=8080
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/subscribe"]
