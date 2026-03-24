package cmd

// Version is the current version of gh-stack.
// In release builds, this is overridden at build time via ldflags
// (see .github/workflows/release.yml).
// The "dev" default indicates a local development build.
var Version = "dev"
