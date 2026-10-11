package domain

import (
	"regexp"
	"strings"
)

var (
	e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	// Kenyan national significant numbers are nine digits starting 7 (mobile) or 1 (newer
	// mobile ranges).
	kenyaNational = regexp.MustCompile(`^[71][0-9]{8}$`)
)

// NormalisePhone turns what a person types into E.164. It accepts full international form
// (+254712345678), and the three ways Kenyans write a local mobile number: 0712345678,
// 0112345678 and 254712345678. Spaces, dashes and brackets are ignored. ok is false when the
// input is none of these.
//
// The phone is the M-Pesa identity, so two spellings of one number must become one account.
func NormalisePhone(raw string) (e164 string, ok bool) {
	s := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '(', ')', '\t':
			return -1
		}
		return r
	}, strings.TrimSpace(raw))

	switch {
	case strings.HasPrefix(s, "+"):
		// Already international; checked below.
	case strings.HasPrefix(s, "254") && kenyaNational.MatchString(s[3:]):
		s = "+" + s
	case strings.HasPrefix(s, "0") && kenyaNational.MatchString(s[1:]):
		s = "+254" + s[1:]
	default:
		return "", false
	}

	if !e164Pattern.MatchString(s) {
		return "", false
	}
	// A Kenyan number in international form must still be a real-looking one: +2540712… is
	// the common mistake of keeping the leading zero.
	if strings.HasPrefix(s, "+254") && !kenyaNational.MatchString(s[4:]) {
		return "", false
	}
	return s, true
}
