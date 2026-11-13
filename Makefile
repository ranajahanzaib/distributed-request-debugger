.PHONY: help up down reset logs build fmt test lint demo demo-lock demo-healthy demo-payment demo-timeout demo-all smoke test-integration
help:
	@printf '%s\n' 'up down reset logs build fmt test lint demo-healthy demo-lock demo-payment demo-timeout demo-all smoke test-integration'
up:
	docker compose up --build -d
down:
	docker compose down
reset:
	docker compose down -v
	docker compose up --build -d
logs:
	docker compose logs -f
build:
	go build ./cmd/app ./cmd/debugger
fmt:
	gofmt -w $$(find cmd internal tests -name '*.go')
test:
	go test ./...
lint:
	go vet ./...
demo: demo-lock
demo-lock:
	./scripts/demo-lock.sh
demo-healthy:
	go run ./cmd/debugger demo run healthy
demo-payment:
	go run ./cmd/debugger demo run slow-payment
demo-timeout:
	go run ./cmd/debugger demo run upstream-timeout
demo-all: demo-healthy demo-lock demo-payment demo-timeout
smoke:
	./scripts/smoke.sh
test-integration:
	DDR_INTEGRATION=1 go test -tags=integration -timeout=180s ./tests/integration
