package version

import "os"

// AppName is what this build calls itself on screen and in the frontend's
// launcher list. This fork installs beside upstream Grout, so the two must not
// share a name.
const AppName = "Grout Save Sync"

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

type BuildInfo struct {
	Version   string
	GitCommit string
	BuildDate string
}

func Get() BuildInfo {
	v := Version
	if override := os.Getenv("GROUT_VERSION"); override != "" {
		v = override
	}
	return BuildInfo{
		Version:   v,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
	}
}
