// Package version несёт сборочный штамп, вставляемый через -ldflags.
// Без штампа значения остаются dev/unknown — так видно нештатную сборку.
package version

import "runtime"

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildHash = "unknown"
	BuildDate = "unknown"
)

func GoVersion() string { return runtime.Version() }
