// Package version holds the semantic version of the build.
package version

// Version is the semantic version (vMAJOR.MINOR.PATCH). Release builds
// overwrite it with
//
//	-ldflags "-X github.com/ja7ad/meilisearch-cdc/internal/version.Version=v1.2.3"
//
// so the binary, the dashboard and the logs all report the release tag.
var Version = "v1.0.0"

// String returns the version.
func String() string { return Version }
