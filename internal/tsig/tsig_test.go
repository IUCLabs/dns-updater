package tsig

import "testing"

func TestParse(t *testing.T) {
	in := `key "client-update" {
    algorithm hmac-sha256;
    secret "dGVzdHNlY3JldA==";
};`
	k, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if k.Name != "client-update." {
		t.Errorf("name=%q want client-update.", k.Name)
	}
	if k.Algorithm != "hmac-sha256." {
		t.Errorf("algorithm=%q want hmac-sha256.", k.Algorithm)
	}
	if k.Secret != "dGVzdHNlY3JldA==" {
		t.Errorf("secret=%q", k.Secret)
	}
	if m := k.SecretMap(); m["client-update."] != k.Secret {
		t.Errorf("SecretMap=%v", m)
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		`nonsense`,
		`key "x" { algorithm hmac-sha256; };`,                // no secret
		`key "x" { secret "abc"; };`,                         // no algorithm
		`key "x" { algorithm hmac-whirlpool; secret "a"; };`, // unsupported algo
	} {
		if _, err := Parse(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}
