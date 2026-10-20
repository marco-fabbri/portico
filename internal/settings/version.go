package settings

import (
	"strconv"
	"strings"
)

// VersionAtLeast reports whether a Prism Central version string
// ("pc.7.6.0.6", "7.6") is at least major.minor. The calendar versions
// before pc.7.x ("pc.2024.3") are older than any 7.x; an unreadable
// version is not at least anything.
func VersionAtLeast(v string, major, minor int) bool {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "pc."), ".")
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	mnr, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if maj >= 2000 { // calendar versioning, pc.2022.x to pc.2024.x
		return false
	}
	return maj > major || (maj == major && mnr >= minor)
}
