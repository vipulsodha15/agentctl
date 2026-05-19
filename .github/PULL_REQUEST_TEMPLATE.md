## Summary

<!-- 1-3 bullets: what changed and why. -->

## Test plan

- [ ] `go test -race -count=1 ./...`
- [ ] `gofmt -l .` is empty and `go vet ./...` passes
- [ ] `cd web && npm run typecheck && npm run build` (if UI changed)
- [ ] Manually exercised the change (describe how)

## Notes for reviewers

<!-- Anything reviewers should pay extra attention to: a tricky migration,
a deliberate trade-off, a follow-up that's out of scope, etc. -->

## Related issues

<!-- Closes #..., relates to #... -->
