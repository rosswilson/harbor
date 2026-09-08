.PHONY: up down reset test integration format format-check lint quality

GO_PACKAGES := ./cmd/... ./internal/... ./web/...

RUFF ?= ruff
YAMLLINT ?= yamllint
SHFMT ?= go run mvdan.cc/sh/v3/cmd/shfmt@v3.12.0
STATICCHECK ?= go run honnef.co/go/tools/cmd/staticcheck@v0.6.1

up:
	./scripts/up.sh
down:
	./scripts/down.sh
reset:
	./scripts/down.sh --reset
test:
	go test -race $(GO_PACKAGES)
integration:
	python3 tests/integration.py

format:
	gofmt -w cmd internal web/embed.go
	npm run format
	$(RUFF) check --fix scripts/harborctl tests
	$(RUFF) format scripts/harborctl tests
	$(SHFMT) -w -i 2 scripts/*.sh

format-check:
	@files="$$(gofmt -l cmd internal web/embed.go)"; if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi
	npm run format:check
	$(RUFF) format --check scripts/harborctl tests
	$(SHFMT) -d -i 2 scripts/*.sh

lint:
	go vet $(GO_PACKAGES)
	$(STATICCHECK) $(GO_PACKAGES)
	npm run lint
	$(RUFF) check scripts/harborctl tests
	shellcheck scripts/*.sh
	$(YAMLLINT) compose.yaml infra/envoy.yaml .yamllint.yaml
	docker compose config --quiet
	docker build --check .

quality: format-check lint test
