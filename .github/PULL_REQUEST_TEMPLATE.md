## Summary

<!-- Brief description of changes -->

## Changes

<!-- List the specific changes made -->

-

## Type of Change

<!-- Check the relevant option -->

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to change)
- [ ] Documentation update
- [ ] Refactoring (no functional changes)
- [ ] Chore (maintenance, dependencies, CI, etc.)

## Checklist

- [ ] My code follows the project's code style
- [ ] I have run `make check` (fmt + vet + test)
- [ ] I have added tests for new functionality (if applicable)
- [ ] I have updated documentation (if applicable)
- [ ] My changes do not introduce new warnings

## Related Issues

<!-- Link to related issues: Closes #123, Fixes #456 -->

## CUBRID correctness and regression evidence

<!-- Complete only the items relevant to the code changed; state N/A with a reason for unrelated documentation changes. See docs/ENGINEERING_INVARIANTS.md. -->

- [ ] For changes to connection, CAS handling or transactions, I tested failure/cancellation and stale-handle behavior; no request with uncertain execution is automatically replayed.
- [ ] For changed SQL/value handling, I verified actual CUBRID results (or explicitly marked live evidence as not yet available), including precision and error propagation.
- [ ] I documented the Go version, CUBRID server version and **actual** live/offline tests executed, including skips and failures; I did not treat a successful startup as a DB test.
- [ ] I reviewed public `database/sql`/module compatibility and updated the release notes when user-visible behavior changed.
