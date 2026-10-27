# Local development. Needs Go 1.25+, Node 22+, pnpm and a Postgres 16 you can reach.
DATABASE_URL ?= postgres://postgres@localhost:5432/outflare?sslmode=disable
TEST_DATABASE_URL ?= postgres://postgres@localhost:5432/outflare_test?sslmode=disable
# A fixed key for local development only. Never use it for real mailboxes.
OUTFLARE_SECRET_KEY ?= ZGV2LW9ubHktZGV2LW9ubHktZGV2LW9ubHktZGV2LTA=
export OUTFLARE_SECRET_KEY

.PHONY: run migrate test web build docker

run: ## Run the engine (API + workers) against DATABASE_URL
	DATABASE_URL="$(DATABASE_URL)" go run ./engine/cmd/outflare serve

migrate:
	DATABASE_URL="$(DATABASE_URL)" go run ./engine/cmd/outflare migrate

test:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 -p 1 ./...

web: ## Web app dev server on :5173, proxying the API to :8080
	pnpm --filter @outflare/web dev

build:
	go build -o engine/bin/outflare ./engine/cmd/outflare
	pnpm -r build

docker:
	docker build -t outflare:dev .
