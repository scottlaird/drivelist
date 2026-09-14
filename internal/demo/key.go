package demo

import (
	"fmt"
	"sort"
	"strings"
)

// Key names the file that answers one call: the request's fields as
// "k=v" pairs in key order, joined by commas, "index" when the request
// is empty. It mirrors demoKey() in ui/app.js exactly, so what the page
// asks for is what the export wrote. Unset, false, empty and "since"
// fields are left out: the page always asks for the same windows, so
// the export need not record them.
func Key(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if k == "since" {
			continue
		}
		var text string
		switch v := body[k].(type) {
		case nil:
			continue
		case bool:
			if !v {
				continue
			}
			text = "true"
		case string:
			if v == "" {
				continue
			}
			text = v
		case []string:
			if len(v) == 0 {
				continue
			}
			text = strings.Join(v, "+")
		case int, int32, int64:
			text = fmt.Sprint(v)
		default:
			text = fmt.Sprint(v)
		}
		parts = append(parts, k+"="+encodeURIComponent(text))
	}
	if len(parts) == 0 {
		return "index"
	}
	return strings.Join(parts, ",")
}

// encodeURIComponent is JavaScript's: everything but A-Z a-z 0-9 - _ . !
// ~ * ' ( ) becomes %XX of its UTF-8 bytes.
func encodeURIComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case strings.IndexByte("-_.!~*'()", c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
