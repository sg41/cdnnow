package main

import "testing"

func TestParseNumParam(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int64
		code  int
	}{
		{"simple", "num=5", 5, numOK},
		{"negative", "num=-100", -100, numOK},
		{"zero", "num=0", 0, numOK},
		{"int64max", "num=9223372036854775807", 9223372036854775807, numOK},
		{"int64min", "num=-9223372036854775808", -9223372036854775808, numOK},
		{"among others", "a=1&num=42&b=2", 42, numOK},
		{"first wins", "num=1&num=2", 1, numOK},
		{"first wins bad second ignored", "num=7&num=xyz", 7, numOK},
		{"trailing amp", "num=10&", 10, numOK},
		{"plus becomes space then trimmed", "num=+5", 5, numOK},
		{"escaped plus", "num=%2B5", 5, numOK},
		{"surrounding spaces", "num=%20-7%20", -7, numOK},
		{"empty query", "", 0, numMissing},
		{"other keys", "foo=1&bar=2", 0, numMissing},
		{"no equals dropped like parse_qs", "num", 0, numMissing},
		{"blank value dropped like parse_qs", "num=", 0, numMissing},
		{"blank among others", "num=&num=3", 3, numOK},
		{"non-integer", "num=abc", 0, numBad},
		{"float", "num=3.5", 0, numBad},
		{"only spaces", "num=%20", 0, numBad},
		{"overflow", "num=9999999999999999999999", 0, numBad},
		{"bad escape", "num=%zz", 0, numBad},
		{"empty value with others", "a=&num=x", 0, numBad},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, code := parseNumParam(tt.query)
			if code != tt.code {
				t.Fatalf("parseNumParam(%q) code = %d, want %d", tt.query, code, tt.code)
			}
			if code == numOK && got != tt.want {
				t.Fatalf("parseNumParam(%q) = %d, want %d", tt.query, got, tt.want)
			}
		})
	}
}
