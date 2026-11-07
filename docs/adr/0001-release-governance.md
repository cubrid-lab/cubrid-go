# ADR 0001: Release governance and compatibility contract

- **Status:** Proposed
- **Date:** 2026-11-07
- **Decision scope:** cubrid-go release and supported-consumer contract
- **Owner:** Repository maintainers (approval not yet recorded)

## Context

A tag, passing mock suite, or green job without executed database cases does not establish a ready release of cubrid-go. The release contract follows this repository's own responsibilities as the pure-Go database/sql CAS driver, including go.mod, Go semantic import versioning, database/sql behavior, and real CUBRID tests. External package versions cannot substitute for local compatibility evidence.

## Decision

Use Go module SemVer with an explicit 0.x contract and release v1.0 only after verified database/sql/driver behavior, real CUBRID testing and stable exported module compatibility. Verify the current go.mod module identifier github.com/cubrid-labs/cubrid-go against imports and repository ownership before any changes. For future v2+, use Go Semantic Import Versioning and a /v2 module path. Allow driver releases independently of GORM, and coordinate tested major generations with the ORM without forced lockstep. Validate external Go module consumers and actual CUBRID sessions in release preflight.

Applicable SemVer classification: a compatible bug/security fix is generally PATCH, an additive feature MINOR, and an incompatible public behavior or supported-environment change requires a reviewed breaking release (under current 0.x rules, commonly the next 0.y.0; at 1.x+ a MAJOR). Changes to SQL generation, data types, migrations or transactions can be compatibility-breaking without exported-type changes. A release candidate must have a reviewed version and notes, a final immutable commit, actual live CUBRID evidence when database behavior is claimed, and verified published artifacts. Keep omitted/failed checks visible; never invent approval or successful publication.

## Alternatives considered

- **Rejected:** Impose a runtime/version rule unrelated to Go module compatibility; mechanically match another package's full version; remove existing module import paths without migration; call the full CI matrix passed when the supported servers were not all tested.
- **Rejected:** Making this package's releases contingent on matching another repository's version number or calendar.
- **Rejected:** Treat an issue, green mock suite, GitHub tag or draft changelog as sufficient release evidence.

## Consequences and trade-offs

Avoids breaking go get and database/sql callers, but a module-path reconciliation and expanded CUBRID release tests may delay v1.

Maintainers must explicitly review the supported matrix and any runtime/API removal. Compatible fixes may be released independently when their own gates pass. Release automation can prepare PRs but must not bypass a maintainer review, exact candidate test or registry verification.

## Verification / acceptance criteria

- [ ] Maintainer reviews and accepts this ADR, links the approval and records acceptance date.
- [ ] Published README/release guide and dependency version declarations agree with the reviewed compatibility contract.
- [ ] A candidate-specific CI check identifies supported CUBRID server versions, tests run/passed/failed/skipped, runtime and driver/ORM versions, and actual artifact.
- [ ] Required release checks reject failed, missing, zero-case or skipped live tests, with scoped exceptions documented as unsupported/unverified—not counted as passes.
- [ ] User-visible breaking changes have accurate migration notes; correct author attribution in curated release notes comes from verified merged contributions.
- [ ] Existing immutable tags and published versions remain untouched by adopting this ADR.

## References

[release policy](../RELEASE_POLICY.md), [Go API policy #57](https://github.com/cubrid-lab/cubrid-go/issues/57), [release verification #63](https://github.com/cubrid-lab/cubrid-go/issues/63)

**This ADR does not update version numbers, create a release, authorize publishing, or claim any database test has passed.** Its status remains **Proposed** until explicit maintainer approval.
