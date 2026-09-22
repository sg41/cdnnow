package main

import (
	"net/url"
	"strconv"
	"strings"
)

const (
	numOK = iota
	numMissing
	numBad
)

// parseNumParam extracts the first "num" query parameter, mirroring
// urllib.parse.parse_qs(...).get("num", [None])[0] + int(...) from Python:
// missing -> numMissing, present but not an integer -> numBad.
func parseNumParam(rawQuery string) (int64, int) {
	for len(rawQuery) > 0 {
		var kv string
		if i := strings.IndexByte(rawQuery, '&'); i >= 0 {
			kv = rawQuery[:i]
			rawQuery = rawQuery[i+1:]
		} else {
			kv = rawQuery
			rawQuery = ""
		}
		var k, v string
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
			v = kv[i+1:]
		} else {
			// No '=': parse_qs drops such pairs -> not "num".
			continue
		}
		if k != "num" || v == "" {
			// parse_qs with keep_blank_values=False drops blank values.
			continue
		}
		// Manual scan avoids allocating url.Values map per request.
		uv, err := url.QueryUnescape(v)
		if err != nil {
			return 0, numBad
		}
		// Python int() tolerates surrounding whitespace.
		n, err := strconv.ParseInt(strings.TrimSpace(uv), 10, 64)
		if err != nil {
			return 0, numBad
		}
		return n, numOK
	}
	return 0, numMissing
}
