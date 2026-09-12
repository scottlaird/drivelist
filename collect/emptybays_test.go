package collect

import "testing"

func TestIsExpanderOwnBay(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/sys/devices/pci0000:00/host4/port-4:0/expander-4:0/sas_device/expander-4:0/bay_identifier", true},
		{"/sys/devices/pci0000:00/host4/port-4:0/expander-4:0/port-4:0:2/end_device-4:0:2/sas_device/end_device-4:0:2/bay_identifier", false},
	}
	for _, tc := range tests {
		if got := isExpanderOwnBay(tc.path); got != tc.want {
			t.Errorf("isExpanderOwnBay(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
