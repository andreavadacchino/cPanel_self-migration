package cpanel

import "testing"

func TestParseEnsureResult(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		wantErr bool
		state   AccountState
		bakdir  string
	}{
		{
			name:  "created no backup",
			out:   "CREATED\n",
			state: AccountCreated,
		},
		{
			name:  "updated",
			out:   "UPDATED\n",
			state: AccountUpdated,
		},
		{
			name:   "created after orphan dir renamed",
			out:    "BAKDIR homelab-bak\nCREATED\n",
			state:  AccountCreated,
			bakdir: "homelab-bak",
		},
		{
			name:   "created after numbered backup",
			out:    "BAKDIR homelab-bak.3\nCREATED\n",
			state:  AccountCreated,
			bakdir: "homelab-bak.3",
		},
		{
			name:    "add_pop failure",
			out:     "ACCTFAIL some uapi error\n",
			wantErr: true,
		},
		{
			name:    "rename failure surfaces as error",
			out:     "ACCTFAIL could not rename orphan maildir /home/x/mail/d/u\n",
			wantErr: true,
		},
		{
			name:    "garbage",
			out:     "weird\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := parseEnsureResult(tc.out, "homelab", "domain4.example")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got res=%+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.State != tc.state {
				t.Errorf("state = %q, want %q", res.State, tc.state)
			}
			if res.BackedUpDir != tc.bakdir {
				t.Errorf("bakdir = %q, want %q", res.BackedUpDir, tc.bakdir)
			}
		})
	}
}
