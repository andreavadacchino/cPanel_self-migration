package model

import "testing"

func TestActionFor(t *testing.T) {
	cases := []struct {
		src    DomainType
		exists bool
		want   Action
	}{
		{Main, false, CreateAddon},
		{Addon, false, CreateAddon},
		{Parked, false, CreateAddon},
		{Sub, false, CreateSub},
		{Main, true, AlreadyPresent},
		{Addon, true, AlreadyPresent},
		{Sub, true, AlreadyPresent},
		{Parked, true, AlreadyPresent},
	}
	for _, c := range cases {
		if got := ActionFor(c.src, c.exists); got != c.want {
			t.Errorf("ActionFor(%v, exists=%v) = %v, want %v", c.src, c.exists, got, c.want)
		}
	}
}

func TestCompatibleDestinationType(t *testing.T) {
	cases := []struct {
		name string
		src  DomainType
		dest DomainType
		want bool
	}{
		{"main to addon", Main, Addon, true},
		{"addon to addon", Addon, Addon, true},
		{"parked to addon", Parked, Addon, true},
		{"sub to sub", Sub, Sub, true},
		{"main to main", Main, Main, false},
		{"addon to sub", Addon, Sub, false},
		{"sub to addon", Sub, Addon, false},
		{"parked to parked", Parked, Parked, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CompatibleDestinationType(c.src, c.dest); got != c.want {
				t.Fatalf("CompatibleDestinationType(%v, %v) = %v, want %v", c.src, c.dest, got, c.want)
			}
		})
	}
}

func TestHashScheme(t *testing.T) {
	cases := map[string]string{
		"$6$rfzE0OGZ$Xqn":    "SHA-512",
		"$5$abc$def":         "SHA-256",
		"$2y$10$abc":         "bcrypt",
		"$2a$10$abc":         "bcrypt",
		"$1$abc$def":         "MD5 (weak)",
		"$y$j9T$abc":         "yescrypt",
		"$argon2id$v=19$abc": "Argon2",
		"!locked":            "LOCKED/none",
		"*disabled":          "LOCKED/none",
		"":                   "EMPTY",
		"plaintextnonsense":  "unknown",
	}
	for in, want := range cases {
		if got := HashScheme(in); got != want {
			t.Errorf("HashScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDomainTypeString(t *testing.T) {
	cases := map[DomainType]string{
		Main: "main", Addon: "addon", Sub: "sub", Parked: "parked",
	}
	for dt, want := range cases {
		if got := dt.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", dt, got, want)
		}
	}
}

func TestMailboxEmail(t *testing.T) {
	m := Mailbox{Domain: "domain4.example", User: "info"}
	if m.Email() != "info@domain4.example" {
		t.Errorf("Email() = %q", m.Email())
	}
}
