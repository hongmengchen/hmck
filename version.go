package main

import (
	"strconv"
	"strings"
)

// isNewer 比较点分版本号：isNewer("0.2.0", "0.1.9") == true
func isNewer(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if na != nb {
			return na > nb
		}
	}
	return false
}
