package config

import (
	"log/slog"
	"strings"
	"testing"
)

func load(t *testing.T, kv map[string]string) (*Config, error) {
	t.Helper()
	return Load(Source{LookupEnv: func(k string) (string, bool) { v, ok := kv[k]; return v, ok }})
}

func TestDefaults(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "./frame-data" || c.Listen != ":8080" || c.Name != "Frame" || c.Registration != "invite" || !c.UserInvites || c.Admin != "on" || c.AdminListen != "" ||
		!c.AuthEmail || !c.AuthPhone || !c.AuthApple || !c.AuthGoogle || c.Storage != "local" || !c.S3Encrypt ||
		c.PushRelay != "https://frame.crow.sg" || c.InviteBase != "https://frame.crow.sg/i" || c.MaxUploadBytes != 50<<20 ||
		c.MaxVideoMS != 15000 || c.MaxFriends != 0 || c.LogLevel != slog.LevelInfo || c.S3Prefix != "frame/" || c.S3Region != "auto" {
		t.Fatalf("defaults: %+v", c)
	}
	if len(c.AppleAudiences) != 2 || c.AppleAudiences[1] != "com.crowtech.frame.signin" {
		t.Fatalf("apple audiences: %v", c.AppleAudiences)
	}
	if c.GoogleEnabled() {
		t.Fatal("google must be off until client ids are configured")
	}
	if c.SMTPEnabled() || c.PublicURL != "" {
		t.Fatal("smtp/public url")
	}
	if len(c.Explicit) != 0 {
		t.Fatalf("defaults marked explicit: %v", c.Explicit)
	}
}

func TestExplicitKeysAndAdmin(t *testing.T) {
	file, _ := ParseFile(strings.NewReader("FRAME_NAME=x\nFRAME_MAX_FRIENDS=5\n"))
	c, err := Load(Source{File: file, Overrides: map[string]string{"FRAME_REGISTRATION": "open"}, LookupEnv: func(k string) (string, bool) {
		switch k {
		case "FRAME_USER_INVITES":
			return "false", true
		case "FRAME_ADMIN":
			return "local", true
		case "FRAME_ADMIN_LISTEN":
			return "127.0.0.1:8081", true
		}
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"FRAME_NAME", "FRAME_MAX_FRIENDS", "FRAME_REGISTRATION", "FRAME_USER_INVITES", "FRAME_ADMIN"} {
		if !c.Explicit[k] {
			t.Errorf("%s not explicit", k)
		}
	}
	if c.Explicit["FRAME_MAX_VIDEO_SECONDS"] || c.UserInvites || c.Admin != "local" || c.AdminListen != "127.0.0.1:8081" || c.Registration != "open" {
		t.Fatalf("%+v", c)
	}
	for _, kv := range []map[string]string{{"FRAME_ADMIN": "sometimes"}, {"FRAME_ADMIN_LISTEN": "8081"}, {"FRAME_USER_INVITES": "maybe"}} {
		if _, err := load(t, kv); err == nil {
			t.Errorf("accepted %v", kv)
		}
	}
}

func TestOverridesAndPrecedence(t *testing.T) {
	file, err := ParseFile(strings.NewReader("# comment\n\nFRAME_NAME=\"Daniel's Frame\"\nexport FRAME_LISTEN=:9000\nFRAME_PUSH_RELAY=\nFRAME_DOMAIN=frame.example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(Source{File: file, LookupEnv: func(k string) (string, bool) {
		if k == "FRAME_LISTEN" {
			return ":7000", true
		}
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Daniel's Frame" || c.Listen != ":7000" || c.PushRelay != "" || c.PublicURL != "https://frame.example.com" {
		t.Fatalf("%+v", c)
	}
	c, err = load(t, map[string]string{"FRAME_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.1, cloudflare", "FRAME_AUTH_METHODS": "email",
		"FRAME_MAX_UPLOAD_MB": "10", "FRAME_LOG_LEVEL": "debug", "FRAME_GOOGLE_CLIENT_IDS": "a,b", "FRAME_REGISTRATION": "INVITE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 2 || !c.TrustCloudflare || c.AuthPhone || c.MaxUploadBytes != 10<<20 || c.LogLevel != slog.LevelDebug ||
		c.GoogleEnabled() || len(c.GoogleClientIDs) != 2 || c.Registration != "invite" || c.TrustedProxies[1].String() != "192.168.1.1/32" {
		t.Fatalf("%+v", c)
	}
}

func TestValidation(t *testing.T) {
	bad := []map[string]string{
		{"FRAME_REGISTRATION": "maybe"},
		{"FRAME_STORAGE": "ftp"},
		{"FRAME_STORAGE": "s3"},
		{"FRAME_STORAGE": "s3", "FRAME_S3_ENDPOINT": "http://r2.example.com", "FRAME_S3_BUCKET": "b", "FRAME_S3_ACCESS_KEY_ID": "k", "FRAME_S3_SECRET_ACCESS_KEY": "s"},
		{"FRAME_PUSH_RELAY": "http://relay.example.com"},
		{"FRAME_MAX_UPLOAD_MB": "0"},
		{"FRAME_MAX_UPLOAD_MB": "ten"},
		{"FRAME_MAX_VIDEO_SECONDS": "601"},
		{"FRAME_MAX_FRIENDS": "-1"},
		{"FRAME_TRUSTED_PROXIES": "not-a-cidr"},
		{"FRAME_AUTH_METHODS": "email,carrier-pigeon"},
		{"FRAME_LOG_LEVEL": "loud"},
		{"FRAME_PUBLIC_URL": "ftp://x"},
		{"FRAME_DOMAIN": "https://frame.example.com"},
		{"FRAME_SMTP_URL": "smtps://u:p@mail.example.com:465"},
		{"FRAME_S3_ENCRYPT": "perhaps"},
		{"FRAME_DATA_DIR": " "},
	}
	for _, kv := range bad {
		if _, err := load(t, kv); err == nil {
			t.Errorf("accepted %v", kv)
		}
	}
	ok := map[string]string{"FRAME_STORAGE": "s3", "FRAME_S3_ENDPOINT": "https://acct.r2.cloudflarestorage.com/", "FRAME_S3_BUCKET": "b",
		"FRAME_S3_ACCESS_KEY_ID": "k", "FRAME_S3_SECRET_ACCESS_KEY": "s", "FRAME_PUSH_RELAY": "http://127.0.0.1:9999", "FRAME_S3_ENCRYPT": "false",
		"FRAME_SMTP_URL": "smtp://u:p@mail.example.com:587", "FRAME_SMTP_FROM": "frame@example.com"}
	c, err := load(t, ok)
	if err != nil {
		t.Fatal(err)
	}
	if c.S3Endpoint != "https://acct.r2.cloudflarestorage.com" || c.S3Encrypt || !c.SMTPEnabled() {
		t.Fatalf("%+v", c)
	}
	// Secrets never appear in error messages.
	_, err = load(t, map[string]string{"FRAME_STORAGE": "s3", "FRAME_S3_ENDPOINT": "ftp://x", "FRAME_S3_BUCKET": "b", "FRAME_S3_ACCESS_KEY_ID": "AKID", "FRAME_S3_SECRET_ACCESS_KEY": "SUPERSECRET"})
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("err %v", err)
	}
}

func TestParseFileErrors(t *testing.T) {
	for _, s := range []string{"NOEQUALS", "lower=1", "1ABC=x", "=x"} {
		if _, err := ParseFile(strings.NewReader(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func FuzzParseFile(f *testing.F) {
	f.Add("FRAME_NAME=x\n# c\nFRAME_LISTEN=':1'\n")
	f.Add("export A=\"b\"")
	f.Fuzz(func(t *testing.T, s string) {
		m, err := ParseFile(strings.NewReader(s))
		if err != nil {
			return
		}
		for k := range m {
			if !validKey(k) {
				t.Fatalf("invalid key %q", k)
			}
		}
		_, _ = Load(Source{File: m})
	})
}
