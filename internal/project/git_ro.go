package project

// GitRO runs a read-only git subcommand in dir (fixed args from the caller, never user
// concatenation) under the same timeout, output cap and no-optional-locks rules as the
// browse layer (runGitRO). A failure is returned as it came out of the process, so a caller
// that must tell a git "no" (exit status 1, e.g. merge-base --is-ancestor) from a git that
// could not run can inspect it with errors.As on *exec.ExitError.
func GitRO(dir string, capBytes int, args ...string) (string, error) {
	return runGitRO(dir, capBytes, args...)
}
