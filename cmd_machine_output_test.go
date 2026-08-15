package velocity

import "testing"

func TestMachineOutputRequested(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		{"routes with json", []string{"routes", "--json"}, true},
		{"routes without json", []string{"routes"}, false},
		{"routes with other flag", []string{"routes", "--verbose"}, false},
		{"json on other command", []string{"migrate", "--json"}, false},
		{"empty argv", nil, false},
		{"json before routes", []string{"--json", "routes"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := machineOutputRequested(tt.argv); got != tt.want {
				t.Errorf("machineOutputRequested(%v) = %v, want %v", tt.argv, got, tt.want)
			}
		})
	}
}
