package driver

import "flag"

// update mirrors the flag internal/driver/drivertest declares, so that the repository-wide golden
// sweep -- `go test ./internal/driver/... -update`, which make golden-update runs -- parses in
// this package's test binary as well. This package compiles nothing and owns no golden files, but
// it does have tests, so without a matching declaration its binary rejects the flag with
// "flag provided but not defined" and takes the whole target down with it.
//
// The alternative was narrowing the target to the packages that own golden files, which a fourth
// driver would then have to remember to join.
//
// This package must never import drivertest, not even from an external test package: two
// declarations of one flag name in a single binary panic at init.
var _ = flag.Bool("update", false, "unused here; internal/driver/drivertest owns the golden files")
