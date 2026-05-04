# Build image
FROM golang:1.23-alpine AS build

WORKDIR /app

COPY . .
RUN go mod download && CGO_ENABLED=0 go build -o ./build/gau ./cmd/gau

# Release image
FROM alpine:3.19

RUN apk -U upgrade --no-cache && apk add --no-cache ca-certificates
COPY --from=build /app/build/gau /usr/local/bin/gau

RUN adduser \
    --gecos "" \
    --disabled-password \
    gau

USER gau
ENTRYPOINT ["gau"]
