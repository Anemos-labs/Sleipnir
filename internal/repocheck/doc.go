// Package repocheck keeps the repository's linked files in step, as ordinary tests; it has no code of its own.
//
// Some things in this repository are copies of each other, or are named in each other: the job that branch protection
// requires and the job that ci.yml defines, the platforms goreleaser ships and the ones ci builds, the files CODEOWNERS
// names and the files that exist, the links in the documents and what they point to. A rename on one side that misses the
// other does not fail a build; it silently stops protecting something, or blocks every pull request. Each test here reads
// the files involved (relative to the module root) and fails with the file, the line and the one thing to change.
//
// The tests read only files of this checkout and use only the standard library; there is no YAML parser in the
// dependency set, so the workflow files are read line by line (yaml_test.go), which is why they are kept in the plain block
// style the repository uses. Outside a checkout of the repository (the module cache, a copied tree) they skip with a
// message that says so. The same rules run as scripts where a shell is the right tool (scripts/check-pins.sh,
// scripts/check-deps.sh, scripts/check-declared.sh), so that a developer, a hook and CI agree.
package repocheck
