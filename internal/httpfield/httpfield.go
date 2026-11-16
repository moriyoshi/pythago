// Package httpfield checks HTTP response fields from Python applications (package asgi
// and package wsgi). Malformed fields are a long-standing source of response splitting,
// request smuggling and cache poisoning, so the servers reject them rather than repair
// them.
package httpfield

// tokenChars are the bytes of an RFC 9110 token (field names).
var tokenChars = func() (t [256]bool) {
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		t[c], t[c-'a'+'A'] = true, true
	}
	for _, c := range "!#$%&'*+-.^_`|~" {
		t[c] = true
	}
	return
}()

// ValidName reports whether s is a field name: a non-empty RFC 9110 token.
func ValidName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !tokenChars[s[i]] {
			return false
		}
	}
	return true
}

// ValidValue rejects control characters (CR, LF and NUL among them) except horizontal
// tab, and DEL. Bytes 0x80-0xFF (obs-text) pass, as HTTP allows.
func ValidValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}
