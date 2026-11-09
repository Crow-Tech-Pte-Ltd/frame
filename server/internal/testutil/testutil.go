// Package testutil holds helpers shared by tests in every package.
//
// It registers the -update flag so that
//
//	go test ./... -run TestWriteFixtures -update
//
// works: `go test` forwards unknown flags to every test binary, so every
// package that has tests must define the flag. Each package's tests import
// this package (blank import in flags_test.go) to do so.
package testutil

import "flag"

// Update is set by `go test -update` and asks golden-file tests to rewrite
// their fixtures instead of comparing against them.
var Update = flag.Bool("update", false, "rewrite golden fixtures (docs/fixtures)")
