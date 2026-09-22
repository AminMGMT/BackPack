// Package spec holds the vocabulary the rest of internal/manage is written in:
// what a transport is, what an address and a port look like, and where a tunnel
// binds.
//
// It exists because `internal/manage` is 26,000 lines that cannot be split. The
// measured reason is a cycle — `config.go`, `edit.go` and `directrender.go`
// each call into the other two, so none of them can be lifted on its own — and
// under that cycle sits a layer that has no cycle at all: forty-odd small pure
// functions that everything uses and that use nothing. Those are here.
//
// Lifting them first is what makes the rest possible. Every one of them was
// reachable from every one of the sixty-two files in that package, so any
// attempt to move a larger piece dragged them along and the cut never came
// clean. With the vocabulary underneath rather than inside, the cycle that is
// left is the real one and is the size it actually is.
//
// # Nothing here knows about a tunnel
//
// That is the rule, and it is what keeps this package a leaf. A function here
// answers a question about a string — is this a port, is this transport carried
// in datagrams, what host did the operator name — and never reads a file, runs
// a command or looks at a config. `ValidateConfigFile` is the one thing that
// touches a file, and it does nothing but parse it and answer.
//
// # The names are exported and the callers did not change
//
// `internal/manage` re-declares every one of them under the unexported name it
// had, in spec_alias.go, exactly as core_alias.go does for internal/manage/core.
// Six packages and the CLI call into manage, and a split that renamed anything
// would be a split that touched all of them.
package spec
