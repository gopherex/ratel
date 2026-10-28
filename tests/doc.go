// Package tests holds the ratel integration and end-to-end suites.
//
// Tests that need infrastructure run behind the `integration` build tag
// (make test-integration) and start their own containers through
// testcontainers-go; nothing in this module needs Docker without that tag.
package tests
