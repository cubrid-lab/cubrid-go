# Development setup — Go CUBRID database/sql driver

> **Status:** Derived from `go.mod`, `Makefile`, CI and checked-in Compose. Commands are instructions, **not evidence of executed tests**. [ADR 0005](adr/0005-development-setup.md) is Proposed.

## Dependencies and offline quickstart

- Go **1.21 or later**, matching `go.mod` and CI's Go 1.21/1.22 lanes; a current developer Go toolchain may be newer, but older-version support requires CI evidence.
- Optional Docker Compose for a real CUBRID broker. No Python/Node setup is required to run this driver.
- **Important:** the GitHub repository is `cubrid-lab/cubrid-go`, but `go.mod` says `module github.com/cubrid-labs/cubrid-go`. That path is part of an external Go consumer contract. **Do not fix it by renaming imports in a setup PR**; verify module proxy behavior, then design a compatible migration in [#57](https://github.com/cubrid-lab/cubrid-go/issues/57).

```sh
git clone https://github.com/cubrid-lab/cubrid-go.git
cd cubrid-go
go version
go mod download
go vet ./...
go test -short ./...
```

Optional checks: `go test -race -short ./...`, `go build ./...`, `make doctor`. Avoid `make check` as a **read-only** setup check: the current target depends on `make fmt` which invokes `gofmt -w .` and rewrites files. For a nonmutating formatting check, inspect `gofmt -l .` output; run a formatter deliberately when implementing code.

## Real CUBRID on an isolated local environment

The repository contains `docker-compose.yml` using `cubrid/cubrid:${CUBRID_VERSION:-11.2}`, `CUBRID_DB=testdb` and host port 33000. The service health check executes SQL using `csql`; also verify the actual broker is reachable before treating the integration suite as executed.

```sh
docker compose up -d
export CUBRID_TEST_DSN='CUBRID:localhost:33000:testdb:::'
go test -v -run Integration ./...
docker compose down
```

Use a disposable test DB only. Existing CI has CUBRID 11.0 and 11.2 lanes, not comprehensive 10.2–11.4 evidence. Record actual test names/case counts, Go version, database version and skips. An integration test job that executes zero meaningful queries is not passing DB evidence. Do not infer that a healthy container alone establishes correctness.

## Integration with the ORM and distribution

`cubrid-go` owns `database/sql` CAS communication, while `gorm-cubrid` is the proposed canonical GORM dialect. Avoid adding more GORM logic to the core driver before the compatibility migration is decided. External consumer tests must use the **actual module import path**, including the repository/module mismatch; Go v2+ requires `/v2`.

## Setup readiness criteria

- [ ] Go toolchain and module dependencies resolve on a clean checkout, without forced `go mod tidy` changes.
- [ ] `go vet`, unit tests and relevant race check succeed, with evidence.
- [ ] CUBRID broker and real integration case count are documented separately; no skipped cases reported as passes.
- [ ] Existing module path identity is understood before any change.
- [ ] [Design sign-off PR #61](https://github.com/cubrid-lab/cubrid-go/pull/61), [real DB test issue #63](https://github.com/cubrid-lab/cubrid-go/issues/63) and [release policy](RELEASE_POLICY.md) remain the next gates.
