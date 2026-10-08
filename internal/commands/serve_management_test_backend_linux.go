//go:build linux

package commands

import "github.com/inhere/gofer/internal/servicemgr"

func testManagedBackend() servicemgr.Backend { return servicemgr.BackendSystemdUser }

// testRegisterScopeArgs selects the scope that testManagedBackend expects; the
// default systemd system scope would also require --run-as.
func testRegisterScopeArgs() []string { return []string{"--scope", "user"} }
