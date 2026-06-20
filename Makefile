# Workspace-local Go build cache keeps the sandbox happy.
GOCACHE ?= /private/tmp/camel-gocache
GO := GOCACHE=$(GOCACHE) go

COMPOSE := docker compose -f docker-compose.integration.yml

# azure-sql-edge generates a self-signed cert with a random (sometimes negative)
# serial each boot; Go 1.24 rejects negative serials, so trust the cert and relax
# the x509 check. Harmless for throwaway test databases.
MSSQL_DSN_OPTS := encrypt=disable&TrustServerCertificate=true
MSSQL_GODEBUG  := GODEBUG=x509negativeserial=1

# Live-database DSNs matching docker-compose.integration.yml.
PG_DSN    := postgres://camel:secret@localhost:55432/camel?sslmode=disable
MYSQL_DSN := camel:secret@tcp(127.0.0.1:33060)/camel
MSSQL_DSN := sqlserver://sa:Camel_Test_123@localhost:14330?database=camel&$(MSSQL_DSN_OPTS)

MSSQL_SA_PASSWORD := Camel_Test_123

# DSNs for the persistent containers already running in local Docker (mysql 3306,
# postgres 5433, mssql 1433). Used by `make integration-local`.
LOCAL_PG_DSN    := postgres://postgres:secret@localhost:5433/camel_test?sslmode=disable
LOCAL_MYSQL_DSN := root:secret@tcp(127.0.0.1:3306)/camel_test
LOCAL_MSSQL_DSN := sqlserver://sa:Camel_Test_123@localhost:1433?database=camel_test&$(MSSQL_DSN_OPTS)

.PHONY: build build-all dist-clean test vet fmt integration integration-up integration-test \
	integration-down mssql-up local-setup integration-local

build:
	$(GO) build ./...

# Cross-compile for all supported platforms into dist/.
# -ldflags="-s -w" strips debug info for smaller binaries.
build-all:
	mkdir -p dist
	GOOS=darwin  GOARCH=amd64 $(GO) build -ldflags="-s -w" -o dist/camel-darwin-amd64     ./cmd/camel
	GOOS=darwin  GOARCH=arm64 $(GO) build -ldflags="-s -w" -o dist/camel-darwin-arm64     ./cmd/camel
	GOOS=linux   GOARCH=amd64 $(GO) build -ldflags="-s -w" -o dist/camel-linux-amd64      ./cmd/camel
	GOOS=linux   GOARCH=arm64 $(GO) build -ldflags="-s -w" -o dist/camel-linux-arm64      ./cmd/camel
	GOOS=windows GOARCH=amd64 $(GO) build -ldflags="-s -w" -o dist/camel-windows-amd64.exe ./cmd/camel
	cd dist && sha256sum * > checksums.txt

dist-clean:
	rm -rf dist

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w *.go cmd/camel/*.go

# Full live run: bring the databases up, exercise every driver, tear them down.
integration: integration-up integration-test integration-down

integration-up:
	$(COMPOSE) up -d --wait postgres mysql
	$(COMPOSE) up -d mssql
	@echo "waiting for SQL Server and creating the camel database..."
	@for i in $$(seq 1 40); do \
		if sqlcmd -S tcp:localhost,14330 -U sa -P "$(MSSQL_SA_PASSWORD)" -C -l 2 \
			-Q "IF DB_ID('camel') IS NULL CREATE DATABASE camel;" >/dev/null 2>&1; then \
			echo "SQL Server ready"; break; \
		fi; \
		if [ $$i -eq 40 ]; then echo "SQL Server did not come up in time" >&2; exit 1; fi; \
		sleep 3; \
	done

# Run only the integration tests, against whatever databases are already up.
integration-test:
	$(MSSQL_GODEBUG) \
	CAMEL_TEST_POSTGRES_DSN='$(PG_DSN)' \
	CAMEL_TEST_MYSQL_DSN='$(MYSQL_DSN)' \
	CAMEL_TEST_MSSQL_DSN='$(MSSQL_DSN)' \
	$(GO) test -run Integration -v ./...

integration-down:
	$(COMPOSE) down -v

# --- Persistent local containers (not the compose stack) ---------------------

# Create a standalone SQL Server (azure-sql-edge, arm64-friendly) on :1433 that
# survives restarts, matching the convention of the existing mysql/postgres
# containers. Then create the camel_test database.
mssql-up:
	@if ! docker inspect mssql >/dev/null 2>&1; then \
		docker run -d --name mssql --restart unless-stopped \
			-e ACCEPT_EULA=1 -e MSSQL_SA_PASSWORD='$(MSSQL_SA_PASSWORD)' \
			-p 1433:1433 mcr.microsoft.com/azure-sql-edge:latest; \
	elif [ "$$(docker inspect -f '{{.State.Running}}' mssql)" != "true" ]; then \
		docker start mssql; \
	fi
	@for i in $$(seq 1 40); do \
		if sqlcmd -S tcp:localhost,1433 -U sa -P "$(MSSQL_SA_PASSWORD)" -C -l 2 \
			-Q "IF DB_ID('camel_test') IS NULL CREATE DATABASE camel_test;" >/dev/null 2>&1; then \
			echo "SQL Server ready (camel_test)"; break; \
		fi; \
		if [ $$i -eq 40 ]; then echo "SQL Server did not come up in time" >&2; exit 1; fi; \
		sleep 3; \
	done

# Ensure a throwaway camel_test database exists in each persistent container.
local-setup: mssql-up
	docker exec postgres psql -U postgres -tAc "SELECT 1 FROM pg_database WHERE datname='camel_test'" | grep -q 1 || \
		docker exec postgres psql -U postgres -c "CREATE DATABASE camel_test"
	docker exec mysql sh -c 'mysql -uroot -p"$$MYSQL_ROOT_PASSWORD" -e "CREATE DATABASE IF NOT EXISTS camel_test"'

# Run the integration suite against the persistent local containers.
integration-local: local-setup
	$(MSSQL_GODEBUG) \
	CAMEL_TEST_POSTGRES_DSN='$(LOCAL_PG_DSN)' \
	CAMEL_TEST_MYSQL_DSN='$(LOCAL_MYSQL_DSN)' \
	CAMEL_TEST_MSSQL_DSN='$(LOCAL_MSSQL_DSN)' \
	$(GO) test -run Integration -v ./...
