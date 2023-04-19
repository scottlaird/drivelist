package drivelist

import (
	"testing"
)

func TestHumanize(t *testing.T) {
	sizes := []uint64{
		1,
		2,
		1500,
		3000,
		4000000,
		8001482129408,
	}

	results := []string{
		"1 byte",
		"2 bytes",
		"1500 bytes",
		"3 kB",
		"4 MB",
		"8 TB",
	}

	for i, _ := range sizes {
		v := FormatDiskSize(sizes[i])
		if v != results[i] {
			t.Fatalf("FormatDiskSize(%d) = %q, wants %q", sizes[i],v,results[i])
		}
	}
}
