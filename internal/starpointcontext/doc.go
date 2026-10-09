// Package context provides context awareness for Starpoint.
//
// It detects the current execution environment (working directory, git repository,
// project type) and determines which dependencies are relevant for the current context.
//
// The context detection helps Starpoint make intelligent decisions about which
// packages to install or configure based on the project type (Go, Node, Rust, Python, etc.).
package context
