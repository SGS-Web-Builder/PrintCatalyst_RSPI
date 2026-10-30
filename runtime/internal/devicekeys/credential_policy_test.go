package devicekeys

import "testing"

func TestSystemdCredentialPermissions(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mode, uid, gid  uint32
		protected, want bool
	}{
		{"systemd 0440", 0440, 0, 0, true, true},
		{"ordinary 0440", 0440, 0, 0, false, false},
		{"world readable", 0444, 0, 0, true, false},
		{"group writable", 0460, 0, 0, true, false},
		{"wrong group", 0440, 0, 100, true, false},
		{"foreign owner", 0400, 100, 0, true, false},
		{"root private", 0600, 0, 0, false, true},
		{"service private", 0400, 999, 986, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialPermissions(tc.mode, tc.uid, tc.gid, 999, tc.protected); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
