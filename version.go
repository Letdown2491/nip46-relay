package main

import "fmt"

var (
	major = 1
	minor = 0
	patch = 0
	meta  = "beta"
)

func StringVersion() string {
	v := fmt.Sprintf("nip46-relay - %d.%d.%d", major, minor, patch)

	if meta != "" {
		v = fmt.Sprintf("%s-%s", v, meta)
	}

	return v
}
