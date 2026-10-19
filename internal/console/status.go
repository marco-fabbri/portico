package console

import (
	"fmt"
	"strings"
)

// statusLine reports the four units and, when the admin listener
// answers, whether Portico is in setup mode.
func (s *session) statusLine(adminURL string) string {
	parts := make([]string, 0, 4)
	for _, u := range []string{"portico", "keycloak", "postgres", "cloudflared"} {
		state, err := s.sys.Unit(u)
		if err != nil {
			state = "unknown"
		}
		if u == "portico" && adminURL != "" {
			if body, err := s.sys.Health(adminURL + "healthz"); err == nil && strings.Contains(body, "setup") {
				state += " (setup mode)"
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %s", u, state))
	}
	return strings.Join(parts, "   ")
}
