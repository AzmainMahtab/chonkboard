# Chonkboard. `make help` lists every target.
-include .env
export

GO              ?= go
BIN_DIR         := bin
APP             := $(BIN_DIR)/chonkboard
TAILWIND        := $(BIN_DIR)/tailwindcss
TAILWIND_VERSION?= v4.1.18
TEMPL           ?= $(shell command -v templ 2>/dev/null || echo $(shell $(GO) env GOPATH)/bin/templ)
AIR             ?= $(shell command -v air 2>/dev/null || echo $(shell $(GO) env GOPATH)/bin/air)
CSS_IN          := web/css/input.css
CSS_OUT         := web/static/css/app.css

# Anything in a domain package that imports one of these has broken the layer rule.
PG_CHECK_IMAGE := postgres:17-alpine
PG_CHECK_NAME  := chonkboard-pg-check
PG_CHECK_PORT  := 55432

FORBIDDEN_DOMAIN_IMPORTS := go-chi/chi|jmoiron/sqlx|modernc.org/sqlite|a-h/templ|net/http|pressly/goose|caarlos0/env

.DEFAULT_GOAL := help

## help: list the targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## //' | awk -F': ' '{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- toolchain ---

## tools: install templ and air into GOPATH/bin, and Tailwind into bin/
tools: $(TAILWIND)
	$(GO) install github.com/a-h/templ/cmd/templ@latest
	$(GO) install github.com/air-verse/air@latest

$(TAILWIND):
	@mkdir -p $(BIN_DIR)
	@echo ">> fetching tailwindcss $(TAILWIND_VERSION)"
	@curl -sSLf -o $(TAILWIND) \
		"https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(shell uname -s | tr A-Z a-z)-x64"
	@chmod +x $(TAILWIND)

# ----------------------------------------------------------------- codegen ---

## templ: generate Go from .templ files
templ:
	$(TEMPL) generate

## templ-watch: regenerate on change
templ-watch:
	$(TEMPL) generate --watch --proxy=http://localhost:$(or $(APP_PORT),8080)

## css: build the stylesheet
css: $(TAILWIND)
	$(TAILWIND) -i $(CSS_IN) -o $(CSS_OUT) --minify

## css-watch: rebuild the stylesheet on change
css-watch: $(TAILWIND)
	$(TAILWIND) -i $(CSS_IN) -o $(CSS_OUT) --watch

## generate: templ + css
generate: templ css

# ------------------------------------------------------------------- build ---

## build: the single static binary (assets embedded)
build: generate
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(APP) ./cmd/chonkboard

## run: build then run
run: build
	$(APP)

## dev: hot reload on Go, templ and css changes
dev: $(TAILWIND)
	$(TAILWIND) -i $(CSS_IN) -o $(CSS_OUT)
	$(AIR)

# -------------------------------------------------------------- migrations ---
# goose is a library here, not a CLI: the binary owns the one code path that
# migrates, so boot and the command line can never disagree.

## migrate-up: apply pending migrations
migrate-up:
	$(GO) run ./cmd/chonkboard migrate up

## migrate-down: roll back one migration
migrate-down:
	$(GO) run ./cmd/chonkboard migrate down

## migrate-reset: roll every migration back, leaving an empty database
migrate-reset:
	$(GO) run ./cmd/chonkboard migrate reset

## migrate-status: show which migrations have run
migrate-status:
	$(GO) run ./cmd/chonkboard migrate status

## check-postgres: prove the migrations still run on PostgreSQL (needs docker)
check-postgres:
	@echo "== starting a throwaway PostgreSQL =="
	@docker rm -f $(PG_CHECK_NAME) >/dev/null 2>&1 || true
	@docker run -d --rm --name $(PG_CHECK_NAME) \
		-e POSTGRES_PASSWORD=probe -e POSTGRES_DB=chonkboard \
		-p $(PG_CHECK_PORT):5432 $(PG_CHECK_IMAGE) >/dev/null
	@for i in $$(seq 1 60); do \
		docker exec $(PG_CHECK_NAME) pg_isready -U postgres -d chonkboard >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@echo "== applying the real migrations to PostgreSQL =="
	@CHONKBOARD_TEST_POSTGRES='postgres://postgres:probe@127.0.0.1:$(PG_CHECK_PORT)/chonkboard?sslmode=disable' \
		$(GO) test -tags postgres ./internal/platform/database/ -run Postgres -count=1 -v; \
		status=$$?; \
		docker rm -f $(PG_CHECK_NAME) >/dev/null 2>&1 || true; \
		exit $$status

## migrate-create: new migration (make migrate-create NAME=add_labels)
migrate-create:
	@test -n "$(NAME)" || (echo "NAME is required: make migrate-create NAME=add_labels" && exit 1)
	$(GO) run ./cmd/chonkboard migrate create $(NAME)

## seed: create the super admin if there is not one yet
seed:
	$(GO) run ./cmd/chonkboard seed

## seed-user: create a member account (make seed-user EMAIL=a@b.co NAME="A Name")
seed-user:
	@test -n "$(EMAIL)" || (echo 'EMAIL is required: make seed-user EMAIL=a@b.co NAME="A Name"' && exit 1)
	$(GO) run ./cmd/chonkboard seed user "$(EMAIL)" "$(NAME)"

## backup: snapshot the database right now
backup:
	$(GO) run ./cmd/chonkboard backup

# -------------------------------------------------------------------- gates ---

# COVER_PKGS is for the aggregate profile only. It attributes coverage to the
# package that was exercised rather than to the package the test lives in, which
# matters because the handler slices are driven through the assembled router from
# httpserver's tests -- without it they read as barely covered.
#
# It is deliberately NOT on `test`: there, each package's line would report that
# binary's coverage of the whole tree, which is a number close to zero and says
# nothing. Per-package `test` output stays per-package.
COVER_PKGS := ./internal/...,./web/...

## test: race-enabled tests with per-package coverage
test:
	$(GO) test -race -cover ./...

## cover: write and summarise an aggregate coverage profile
#
# No -race here, deliberately. Combining -race with -coverpkg over ./... produces a
# merged profile whose counters are all zero -- it reports 0.0% with 2000 lines of
# profile data, which looks like a broken build rather than a toolchain wart. The
# race detector runs in `test` and `check`; this target only measures.
cover:
	$(GO) test -coverprofile=coverage.out -coverpkg=$(COVER_PKGS) ./...
	@$(GO) tool cover -func=coverage.out | tail -1

## cover-html: open the coverage profile in a browser
cover-html: cover
	$(GO) tool cover -html=coverage.out

## fmt: format Go
fmt:
	$(GO) fmt ./...

## fmt-check: fail if anything is unformatted
fmt-check:
	@unformatted=$$(gofmt -l . | grep -v '_templ.go' || true); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi

## vet: go vet
vet:
	$(GO) vet ./...

## lint: golangci-lint (installed by lint-install)
lint:
	golangci-lint run ./...

## lint-install: install golangci-lint
lint-install:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

## check-arch: fail if a domain package imports a framework
check-arch:
	@failed=0; \
	for pkg in $$($(GO) list ./internal/... 2>/dev/null | grep '/domain$$'); do \
		hits=$$($(GO) list -f '{{range .Imports}}{{println .}}{{end}}' $$pkg | grep -E '$(FORBIDDEN_DOMAIN_IMPORTS)' || true); \
		if [ -n "$$hits" ]; then \
			echo "ARCH: $$pkg imports:"; echo "$$hits" | sed 's/^/  /'; failed=1; \
		fi; \
	done; \
	if [ $$failed -eq 1 ]; then echo "domain packages must not import a framework"; exit 1; fi; \
	echo "check-arch: domain layers are clean"

## check: everything that must pass before finishing
check: fmt-check vet check-arch test
	@echo "check: all gates passed"

# ------------------------------------------------------------------ docker ---

## up: build and start the stack
up:
	docker compose up -d --build

## watch: start the stack in the foreground
watch:
	docker compose up --build

## down: stop the stack
down:
	docker compose down

## logs: follow the app's logs
logs:
	docker compose logs -f app

## clean: remove build output
clean:
	rm -rf $(APP) $(CSS_OUT) coverage.out tmp
	find . -name '*_templ.go' -delete

.PHONY: help tools templ templ-watch css css-watch generate build run dev \
        migrate-up migrate-down migrate-reset migrate-status migrate-create \
        check-postgres cover-html seed seed-user backup \
        test cover fmt fmt-check vet lint lint-install check-arch check \
        up watch down logs clean
