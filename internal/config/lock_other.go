//go:build !unix

package config

// withLock is a plain call where flock does not exist. cats' daemons are
// unix-only (unix sockets throughout), so the only thing built here is tooling
// that reads the config, never two writers sharing it.
func withLock(path string, fn func() error) error { return fn() }
