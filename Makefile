# MailRoutine developer shortcuts. Run `make` (or `make help`) to list them.
.DEFAULT_GOAL := help
.PHONY: help setup install dev dev-api dev-web run build clean \
        hash-password secret backup \
        security test test-go test-web cover cover-go cover-web contract lint fmt tidy check \
        docker-build docker-up docker-down docker-logs docker-restart

COVER_MIN ?= 95
BIN       ?= mailroutine
CONTRACT  := $(CURDIR)/web/.contract

# Local runs keep state in ./data (the container default /data is usually not writable).
DATA_DIR      ?= $(CURDIR)/data
DATABASE_PATH ?= $(DATA_DIR)/mailroutine.db
HTTP_PORT     ?= 8080
DEV_WEB_PORT  ?= 5173

help: ## Show this help
	@awk 'BEGIN{FS=":.*## "; printf "\nUsage: make \033[36m<target>\033[0m\n"} \
	  /^[a-zA-Z0-9_-]+:.*## /{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2} \
	  /^##@/{printf "\n\033[1m%s\033[0m\n", substr($$0,5)}' $(MAKEFILE_LIST); echo

##@ Getting started

setup: ## Create .env with fresh secrets and an admin password hash (asks for the password)
	@test -f .env || { cp .env.example .env; chmod 600 .env; echo "created .env from .env.example (mode 600)"; }
	@grep -Eq '^SESSION_SECRET=.+' .env || { scripts/setenv.sh SESSION_SECRET "$$(go run ./cmd/$(BIN) generate-secret)"; echo "generated SESSION_SECRET"; }
	@grep -Eq '^ENCRYPTION_KEY=.+' .env || { scripts/setenv.sh ENCRYPTION_KEY "$$(go run ./cmd/$(BIN) generate-secret)"; echo "generated ENCRYPTION_KEY (back it up: without it stored Gmail tokens are unrecoverable)"; }
	@grep -Eq '^ADMIN_PASSWORD_HASH=.+' .env || { \
	  line="$$(go run ./cmd/$(BIN) hash-password)" || exit 1; \
	  scripts/setenv.sh ADMIN_PASSWORD_HASH "$${line#ADMIN_PASSWORD_HASH=}"; echo "stored ADMIN_PASSWORD_HASH"; }
	@grep -Eq '^GOOGLE_CLIENT_ID=.+' .env || echo "TODO: put GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET in .env (see docs/oauth-setup.md)"
	@echo "setup done. Next: make dev"

install: ## Download Go modules and install web dependencies
	go mod download
	cd web && npm ci

##@ Run

dev: ## Hot-reload development: Go API on :8080 + Vite UI on :5173 (open http://localhost:5173)
	@test -f .env || { echo "no .env yet - run: make setup"; exit 1; }
	@test -d web/node_modules || { echo "web dependencies missing - run: make install"; exit 1; }
	@echo "UI: http://localhost:$(DEV_WEB_PORT)   API: http://localhost:$(HTTP_PORT)   (Ctrl-C stops both)"
	@$(MAKE) --no-print-directory -j2 dev-api dev-web

# The browser origin in dev is the Vite server, and the API checks Origin against APP_URL,
# so APP_URL must point at it (register http://localhost:5173/api/accounts/google/callback with Google).
dev-api: ## Go server only, reading .env, state in ./data
	@mkdir -p "$(DATA_DIR)"
	DATABASE_PATH="$(DATABASE_PATH)" HTTP_PORT=$(HTTP_PORT) APP_URL="$${APP_URL:-http://localhost:$(DEV_WEB_PORT)}" \
	  go run ./cmd/$(BIN)

dev-web: ## Vite dev server only (proxies /api to the Go server)
	cd web && npm run dev -- --port $(DEV_WEB_PORT)

run: build ## Build everything and run the production binary on :8080 with .env
	@mkdir -p "$(DATA_DIR)"
	DATABASE_PATH="$(DATABASE_PATH)" ./$(BIN)

build: ## Build the UI and the single mailroutine binary
	cd web && npm run build
	@touch web/dist/.gitkeep
	go build -o $(BIN) ./cmd/$(BIN)

##@ Utilities

hash-password: ## Print an ADMIN_PASSWORD_HASH line for a password you type
	@go run ./cmd/$(BIN) hash-password

secret: ## Print a random 32-byte base64 secret
	@go run ./cmd/$(BIN) generate-secret

backup: ## Consistent database snapshot into ./backups/ (set DATABASE_PATH if different)
	@mkdir -p backups
	DATABASE_PATH="$(DATABASE_PATH)" go run ./cmd/$(BIN) backup backups/mailroutine-$$(date +%Y%m%d-%H%M%S).db

clean: ## Remove build output, coverage and recorded fixtures (keeps .env and ./data)
	rm -rf $(BIN) coverage.out web/coverage web/.contract
	find web/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true

##@ Quality

test: test-go test-web ## Run all tests

test-go:
	go vet ./...
	go test -race -count=1 ./...

test-web: contract
	cd web && npm run lint && npm run typecheck && CONTRACT_DIR=$(CONTRACT) npm test

# Merged Go coverage across all packages (a package's tests may exercise another package).
cover-go:
	go test -race -count=1 -coverpkg=./pkg/...,./internal/...,./cmd/... -coverprofile=coverage.out ./pkg/... ./internal/... ./cmd/...
	@go tool cover -func=coverage.out | tail -1
	@go tool cover -func=coverage.out | awk -v min=$(COVER_MIN) '/^total:/ { gsub("%","",$$3); if ($$3+0 < min) { print "coverage " $$3 "% is below " min "%"; exit 1 } }'

cover-web: contract
	cd web && CONTRACT_DIR=$(CONTRACT) npm run test:coverage

cover: cover-go cover-web ## Coverage for Go (gate: COVER_MIN) and web
	@echo "HTML report: go tool cover -html=coverage.out"

# Record real server responses, then check the UI's schemas against them.
contract:
	rm -rf $(CONTRACT)
	CONTRACT_DIR=$(CONTRACT) go test -count=1 ./internal/http -run TestWriteContractFixtures

lint: ## gofmt, go vet, eslint, tsc
	test -z "$$(gofmt -l .)"
	go vet ./...
	cd web && npm run lint && npm run typecheck

fmt: ## Format Go code
	gofmt -w .

tidy: ## go mod tidy
	go mod tidy

security: ## Dependency vulnerabilities and static analysis (govulncheck, gosec, npm audit)
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@latest -quiet -exclude-dir=web -exclude-dir=external ./...
	cd web && npm audit --omit=dev --audit-level=moderate

check: lint test security ## Everything CI checks (lint + tests + security)

##@ Docker

docker-build: ## Build the image as mailroutine:local
	docker build -t mailroutine:local .

docker-up: ## Start via docker compose (needs .env; uses image from docker-compose.yml)
	@test -f .env || { echo "no .env yet - run: make setup"; exit 1; }
	docker compose up -d

docker-down: ## Stop the compose stack
	docker compose down

docker-restart: ## Restart the container
	docker compose restart

docker-logs: ## Follow container logs
	docker compose logs -f mailroutine
