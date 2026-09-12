package collect

import "testing"

func TestIsDiskName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"sda", true},
		{"sdab", true},
		{"nvme0n1", true},
		{"nvme10n2", true},
		{"nvme0", false},     // controller, not a namespace; not in /sys/block anyway
		{"nvme0c0n1", false}, // hidden multipath path to nvme0n1
		{"nvme1c3n2", false},
		{"nvmen1", false},
		{"loop0", false},
		{"dm-0", false},
		{"md127", false},
		{"sr0", false},
		{"zd0", false},
	}
	for _, tc := range tests {
		if got := isDiskName(tc.name); got != tc.want {
			t.Errorf("isDiskName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
