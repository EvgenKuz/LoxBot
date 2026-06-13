FROM golang:1.26-alpine AS build-stage

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/build/bot

FROM gcr.io/distroless/static-debian13 AS release-stage
LABEL authors="Eugene Smith"

WORKDIR /app

COPY --from=build-stage /app/build/bot /app/bot

EXPOSE 1080
VOLUME ["/app/database"]

CMD ["/app/bot"]