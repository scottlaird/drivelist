package collect

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// parsePlist decodes an XML property list (what diskutil -plist prints)
// into Go values: dict -> map[string]any, array -> []any, string ->
// string, integer -> int64, real -> float64, true/false -> bool, date and
// data -> string. The standard library has no plist package and this is
// all the collector needs.
func parsePlist(r io.Reader) (any, error) {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "plist" {
			v, err := plistValue(dec)
			if err != nil {
				return nil, err
			}
			return v, nil
		}
	}
}

// plistValue reads the next value element and returns it decoded.
func plistValue(dec *xml.Decoder) (any, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return plistElement(dec, t)
		case xml.EndElement:
			return nil, fmt.Errorf("plist: unexpected </%s>", t.Name.Local)
		}
	}
}

func plistElement(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				return m, nil
			case xml.StartElement:
				if t.Name.Local != "key" {
					return nil, fmt.Errorf("plist: expected <key>, got <%s>", t.Name.Local)
				}
				var key string
				if err := dec.DecodeElement(&key, &t); err != nil {
					return nil, err
				}
				v, err := plistValue(dec)
				if err != nil {
					return nil, err
				}
				m[key] = v
			}
		}
	case "array":
		var a []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				if a == nil {
					a = []any{}
				}
				return a, nil
			case xml.StartElement:
				v, err := plistElement(dec, t)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	case "string", "date", "data":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		return strings.TrimSpace(s), nil
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	case "real":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	}
	return nil, fmt.Errorf("plist: unknown element <%s>", se.Name.Local)
}

// dictString and friends read typed values out of a decoded dict.
func dictString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func dictInt(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func dictBool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}

func dictList(m map[string]any, key string) []map[string]any {
	items, _ := m[key].([]any)
	var out []map[string]any
	for _, it := range items {
		if d, ok := it.(map[string]any); ok {
			out = append(out, d)
		}
	}
	return out
}
