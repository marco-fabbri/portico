// Package textedit edits settings files in place, comments kept: the
// console and the exposure apply both rewrite one line of the process
// YAML without a YAML library, which would drop the comments.
package textedit

import "regexp"

// ReplaceFirst expands repl at the first match of re only, and reports
// whether there was one.
func ReplaceFirst(re *regexp.Regexp, text, repl string) (string, bool) {
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return text, false
	}
	var dst []byte
	dst = re.ExpandString(dst, repl, text, loc)
	return text[:loc[0]] + string(dst) + text[loc[1]:], true
}
