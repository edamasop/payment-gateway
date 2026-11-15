# syntax=docker/dockerfile:1

# This module has no workspace-local dependencies, so the build context is the
# gateway directory itself:
#
#   docker build -f payment-gateway/Dockerfile payment-gateway
FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/payment-gateway .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 -H app

USER app
WORKDIR /app

COPY --from=build /out/payment-gateway /app/payment-gateway

EXPOSE 8082
ENTRYPOINT ["/app/payment-gateway"]
