# Development setup — Go CUBRID database/sql driver

> **Status:** Derived from `go.mod`, `Makefile`, CI and checked-in Compose. Commands are instructions, **not evidence of executed tests**. [ADR 0005](adr/0005-development-setup.md) is Proposed.

## Dependencies and offline quickstart

- Go **1.21 or later**, matching `go.mod` and CI's Go 1.21/1.22 lanes; a current developer Go toolchain may be newer, but older-version support requires CI evidence.
- Optional Docker Compose for a real CUBRID broker. The Go module is built and tested with its documented Go toolchain.
- **Module path:** starting with the next 0.x minor release the module path is `github.com/cubrid-lab/cubrid-go`, matching the repository. Releases up to and including `v0.2.1` were published as `github.com/cubrid-labs/cubrid-go`; they stay fetchable under that old path, but new code must import `github.com/cubrid-lab/cubrid-go`. See the upgrade notes in [CHANGELOG.md](../CHANGELOG.md) and [#57](https://github.com/cubrid-lab/cubrid-go/issues/57).

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

The live tests connect to `cubrid://dba:@localhost:33000/testdb?autocommit=true` unless the
test-only `CUBRID_GO_TEST_DSN` variable names another Go `cubrid://` URL. Each test creates its own
tables named `go_test_<purpose>_<random hex>` with a plain `CREATE TABLE` and drops only those in
`t.Cleanup`; it never drops or reuses a pre-existing table (such as a fixed `go_test_basic`), and a
failing cleanup `DROP` fails the test.

Use a disposable test DB only. Existing CI has CUBRID 11.0 and 11.2 lanes, not comprehensive 10.2–11.4 evidence. Record actual test names/case counts, Go version, database version and skips. An integration test job that executes zero meaningful queries is not passing DB evidence. Do not infer that a healthy container alone establishes correctness.

## Integration with the ORM and distribution

`cubrid-go` owns `database/sql` CAS communication, while `gorm-cubrid` is the proposed canonical GORM dialect. Avoid adding more GORM logic to the core driver before the compatibility migration is decided. External consumer tests must use the **actual module import path**, including the repository/module mismatch; Go v2+ requires `/v2`.

## Setup readiness criteria

- [ ] Go toolchain and module dependencies resolve on a clean checkout, without forced `go mod tidy` changes.
- [ ] `go vet`, unit tests and relevant race check succeed, with evidence.
- [ ] CUBRID broker and real integration case count are documented separately; no skipped cases reported as passes.
- [ ] Existing module path identity is understood before any change.
- [ ] [Design sign-off PR #61](https://github.com/cubrid-lab/cubrid-go/pull/61), [real DB test issue #63](https://github.com/cubrid-lab/cubrid-go/issues/63) and [release policy](RELEASE_POLICY.md) remain the next gates.
