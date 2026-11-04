FROM golang:1.26-alpine AS build-stage

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /app/build/bot \
    && mkdir -p /app/build/database

FROM gcr.io/distroless/static-debian13:nonroot AS release-stage
LABEL authors="Eugene Smith"

WORKDIR /app

COPY --from=build-stage /app/build/bot /app/bot
# The database directory belongs to the nonroot user (65532) so the bot can
# write to it; a new named volume inherits this ownership.
COPY --from=build-stage --chown=65532:65532 /app/build/database /app/database

USER 65532:65532

EXPOSE 1080
VOLUME ["/app/database"]

CMD ["/app/bot"]
