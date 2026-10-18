package tenant

import (
	"crypto/rand"
	"errors"
	"math/big"
	"regexp"
	"strings"
)

// ErrInvalidSlug is returned for a slug outside ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$.
var ErrInvalidSlug = errors.New("tenant: invalid slug (lower-case letters, digits and dashes, 1-32 characters, no leading or trailing dash)")

var reSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// ValidSlug reports whether s is an acceptable tenant slug. The slug is
// used as Prism Central project id and in the Keycloak group name.
func ValidSlug(s string) bool { return reSlug.MatchString(s) }

const (
	pwUpper  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	pwLower  = "abcdefghijklmnopqrstuvwxyz"
	pwDigits = "0123456789"
	pwLength = 20
)

// temporaryPassword returns 20 random characters from [A-Za-z0-9] with at
// least one upper-case letter, one lower-case letter and one digit.
func temporaryPassword() (string, error) {
	alphabet := pwUpper + pwLower + pwDigits
	for {
		b := make([]byte, pwLength)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			b[i] = alphabet[n.Int64()]
		}
		s := string(b)
		if strings.ContainsAny(s, pwUpper) && strings.ContainsAny(s, pwLower) && strings.ContainsAny(s, pwDigits) {
			return s, nil
		}
	}
}
