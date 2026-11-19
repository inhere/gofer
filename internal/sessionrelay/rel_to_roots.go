package sessionrelay

// RelToRoots converts a directory a session or seat recorded (a host path such as `D:\...` or a
// container path such as `/d/...`) into the project-relative form, against the project's root
// paths on both sides: the longest root that is a prefix wins and "" is the root itself. ok is
// false when no root is a prefix. It is the one path conversion of the session domain.
func RelToRoots(p string, roots []string) (rel string, ok bool) { return relToRoots(p, roots) }
