FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/app && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/debugger ./cmd/debugger

FROM alpine:3.23.5
RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 app
COPY --from=build /out/service /service
COPY --from=build /out/debugger /debugger
USER app
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=2s --start-period=10s --retries=12 CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/service"]
