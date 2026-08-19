package common

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// ParseByteSizeString parses a human-readable byte size string (e.g. "10GiB", "100MB", "1024")
// into an int64 number of bytes. It supports both SI decimal prefixes (kB, MB, GB, TB, PB, EB)
// and IEC binary prefixes (KiB, MiB, GiB, TiB, PiB, EiB).
func ParseByteSizeString(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty byte size string")
	}

	// Split numeric part and suffix
	var numStr strings.Builder
	var unitStr strings.Builder

	foundUnit := false
	for _, r := range s {
		if !foundUnit && (unicode.IsDigit(r) || r == '.' || r == '+' || r == '-') {
			numStr.WriteRune(r)
		} else {
			foundUnit = true
			unitStr.WriteRune(r)
		}
	}

	numPart := strings.TrimSpace(numStr.String())
	unitPart := strings.TrimSpace(unitStr.String())

	if numPart == "" {
		return 0, fmt.Errorf("invalid byte size string: %q", s)
	}

	val, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing numeric value in %q: %w", s, err)
	}
	if val < 0 {
		return 0, fmt.Errorf("byte size cannot be negative: %q", s)
	}

	var multiplier float64
	u := strings.ToLower(unitPart)
	switch u {
	case "", "b", "bytes":
		multiplier = 1
	case "k", "kb":
		multiplier = 1000
	case "kib":
		multiplier = 1024
	case "m", "mb":
		multiplier = 1000 * 1000
	case "mib":
		multiplier = 1024 * 1024
	case "g", "gb":
		multiplier = 1000 * 1000 * 1000
	case "gib":
		multiplier = 1024 * 1024 * 1024
	case "t", "tb":
		multiplier = 1000 * 1000 * 1000 * 1000
	case "tib":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "p", "pb":
		multiplier = 1000 * 1000 * 1000 * 1000 * 1000
	case "pib":
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
	case "e", "eb":
		multiplier = 1000 * 1000 * 1000 * 1000 * 1000 * 1000
	case "eib":
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unrecognized byte size unit %q in %q", unitPart, s)
	}

	result := int64(val * multiplier)
	return result, nil
}
