package main

import "crypto/sha256"

// sha256Sum wraps crypto/sha256 so main.go stays free of slice-conversion noise.
func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
