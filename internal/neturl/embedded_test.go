package neturl

import (
	"context"
	"errors"
	"testing"
)

func TestIsPrivateHost_EmbeddedLiteral(t *testing.T) {
	ctx := context.Background()
	for _, host := range []string{"::127.0.0.1", "[64:ff9b::7f00:1]", "64:ff9b::a9fe:a9fe", "2002:7f00:1::", "[2606:4700::5efe:10.0.0.1]"} {
		private, err := IsPrivateHost(ctx, nil, host)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", host, err)
		}
		if !private {
			t.Errorf("%s must be private", host)
		}
	}
	for _, host := range []string{"64:ff9b::808:808", "[2002:808:808::1]"} {
		private, err := IsPrivateHost(ctx, nil, host)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", host, err)
		}
		if private {
			t.Errorf("%s carries a public address and must pass", host)
		}
	}
}

func TestValidateURLHost_EmbeddedIPv4(t *testing.T) {
	for _, u := range []string{
		"http://[::127.0.0.1]/",
		"http://[64:ff9b::7f00:1]/",
		"https://[64:ff9b::a9fe:a9fe]/latest/meta-data/",
		"http://[2002:7f00:1::]/",
		"http://[2002:a9fe:a9fe::]:8080/",
	} {
		err := ValidateURLHost(context.Background(), nil, u)
		if !errors.Is(err, ErrPrivateHost) {
			t.Errorf("%s: expected ErrPrivateHost, got %v", u, err)
		}
	}
	if err := ValidateURLHost(context.Background(), nil, "http://[64:ff9b::808:808]/"); err != nil {
		t.Errorf("a public address behind the NAT64 prefix must pass, got %v", err)
	}
}
