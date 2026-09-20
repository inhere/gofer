// Package util holds gofer's small, dependency-free helpers: the allocation
// capacity hints (CapSum/CapMul) and the environment layering used before a child
// process is spawned (Environ/MergeEnv/EnvWith).
//
// It is a leaf package: it imports nothing from gofer and only the standard
// library, so any layer may use it without risking an import cycle (G022). Keep
// it that way — anything with a real domain, a dependency, or more than a
// handful of lines belongs in its own package, not here.
package util
