// Package config loads frame-server configuration from environment variables,
// optionally layered over a KEY=value file (docs/CONFIG.md).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// DefaultGoogleClientIDs are the official Frame app OAuth client ids. They are
// filled in at release time; until then Google sign-in is off unless
// FRAME_GOOGLE_CLIENT_IDS is set.
var DefaultGoogleClientIDs = ""

// Config is the fully parsed and validated server configuration.
type Config struct {
	DataDir     string
	Listen      string
	Domain      string
	ACMEEmail   string
	PublicURL   string   // canonical external URL without trailing slash; "" = derive from request
	PublicHosts []string // all configured public hosts (FRAME_PUBLIC_URL list + FRAME_DOMAIN), canonical form
	// PublicOrigins are the scheme://host origins of the configured public
	// URLs (admin panel CSRF checks accept them besides the request's own).
	PublicOrigins []string
	Name          string

	Registration string // open | invite | closed
	UserInvites  bool   // on invite servers, members' friend-invite links can register new people
	AuthEmail    bool
	AuthPhone    bool
	AuthApple    bool
	AuthGoogle   bool

	AppleAudiences  []string
	GoogleClientIDs []string

	Storage           string // local | s3
	S3Endpoint        string
	S3Bucket          string
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3Prefix          string
	S3Encrypt         bool

	PushRelay  string // "" disables push
	InviteBase string

	MaxUploadBytes int64
	MaxVideoMS     int64
	MaxFriends     int // 0 = unlimited

	TrustedProxies  []netip.Prefix
	TrustCloudflare bool

	SMTPURL  string
	SMTPFrom string

	LogLevel slog.Level

	// Admin is the web admin panel mode: on | local | off (docs/ADMIN.md).
	Admin string
	// AdminListen, when set, serves /admin only on this separate address.
	AdminListen string

	// Explicit holds the keys that were set in the environment, the config
	// file or dev overrides (not defaults). Settings set explicitly are locked
	// in the admin panel.
	Explicit map[string]bool

	// Dev is set by `frame-server dev`: relaxed rate limits, demo seed data.
	Dev bool
}

// Keys lists every supported configuration key.
var Keys = []string{
	"FRAME_DATA_DIR", "FRAME_LISTEN", "FRAME_DOMAIN", "FRAME_ACME_EMAIL", "FRAME_PUBLIC_URL",
	"FRAME_NAME", "FRAME_REGISTRATION", "FRAME_AUTH_METHODS", "FRAME_APPLE_AUDIENCES",
	"FRAME_GOOGLE_CLIENT_IDS", "FRAME_STORAGE", "FRAME_S3_ENDPOINT", "FRAME_S3_BUCKET",
	"FRAME_S3_REGION", "FRAME_S3_ACCESS_KEY_ID", "FRAME_S3_SECRET_ACCESS_KEY", "FRAME_S3_PREFIX",
	"FRAME_S3_ENCRYPT", "FRAME_PUSH_RELAY", "FRAME_INVITE_BASE", "FRAME_MAX_UPLOAD_MB",
	"FRAME_MAX_VIDEO_SECONDS", "FRAME_MAX_FRIENDS", "FRAME_TRUSTED_PROXIES", "FRAME_SMTP_URL",
	"FRAME_SMTP_FROM", "FRAME_LOG_LEVEL", "FRAME_USER_INVITES", "FRAME_ADMIN", "FRAME_ADMIN_LISTEN",
}

var defaults = map[string]string{
	"FRAME_DATA_DIR":          "./frame-data",
	"FRAME_LISTEN":            ":8080",
	"FRAME_NAME":              "Frame",
	"FRAME_REGISTRATION":      "invite",
	"FRAME_USER_INVITES":      "true",
	"FRAME_ADMIN":             "on",
	"FRAME_AUTH_METHODS":      "email,phone,apple,google",
	"FRAME_APPLE_AUDIENCES":   "com.crowtech.frame,com.crowtech.frame.signin",
	"FRAME_STORAGE":           "local",
	"FRAME_S3_REGION":         "auto",
	"FRAME_S3_PREFIX":         "frame/",
	"FRAME_S3_ENCRYPT":        "true",
	"FRAME_PUSH_RELAY":        "https://frame.crow.sg",
	"FRAME_INVITE_BASE":       "https://frame.crow.sg/i",
	"FRAME_MAX_UPLOAD_MB":     "50",
	"FRAME_MAX_VIDEO_SECONDS": "15",
	"FRAME_MAX_FRIENDS":       "0",
	"FRAME_LOG_LEVEL":         "info",
}

// ParseFile parses a KEY=value config file. Blank lines and lines starting
// with '#' are ignored; values may be wrapped in matching single or double
// quotes. Keys must look like environment variable names.
func ParseFile(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("config line %d: expected KEY=value", line)
		}
		k = strings.TrimSpace(k)
		if !validKey(k) {
			return nil, fmt.Errorf("config line %d: invalid key", line)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return out, nil
}

func validKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for i, c := range k {
		if !(c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// Source resolves raw values: real environment first, then file values, then
// defaults. lookupEnv is os.LookupEnv in production.
type Source struct {
	LookupEnv func(string) (string, bool)
	File      map[string]string
	Overrides map[string]string // applied below env, above file (used by dev mode)
}

// explicit reports whether k was set anywhere but the built-in defaults.
func (s Source) explicit(k string) bool {
	if s.LookupEnv != nil {
		if _, ok := s.LookupEnv(k); ok {
			return true
		}
	}
	_, ov := s.Overrides[k]
	_, fv := s.File[k]
	return ov || fv
}

func (s Source) get(k string) (string, bool) {
	if s.LookupEnv != nil {
		if v, ok := s.LookupEnv(k); ok {
			return v, true
		}
	}
	if v, ok := s.Overrides[k]; ok {
		return v, true
	}
	if v, ok := s.File[k]; ok {
		return v, true
	}
	v, ok := defaults[k]
	return v, ok
}

// LoadFile reads the optional config file path and the process environment.
func LoadFile(path string, overrides map[string]string) (*Config, error) {
	src := Source{LookupEnv: os.LookupEnv, Overrides: overrides}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		defer f.Close()
		m, err := ParseFile(f)
		if err != nil {
			return nil, err
		}
		src.File = m
	}
	return Load(src)
}

// Load builds and validates a Config from src.
func Load(src Source) (*Config, error) {
	c := &Config{Explicit: map[string]bool{}}
	for _, k := range Keys {
		if src.explicit(k) {
			c.Explicit[k] = true
		}
	}
	var errs []error
	str := func(k string) string { v, _ := src.get(k); return strings.TrimSpace(v) }

	c.DataDir = str("FRAME_DATA_DIR")
	if c.DataDir == "" {
		errs = append(errs, errors.New("FRAME_DATA_DIR must not be empty"))
	}
	c.Listen = str("FRAME_LISTEN")
	c.Domain = strings.ToLower(str("FRAME_DOMAIN"))
	if c.Domain != "" && (strings.ContainsAny(c.Domain, "/:@ ") || !strings.Contains(c.Domain, ".")) {
		errs = append(errs, errors.New("FRAME_DOMAIN must be a bare host name like frame.example.com"))
	}
	c.ACMEEmail = str("FRAME_ACME_EMAIL")
	c.Name = str("FRAME_NAME")
	if c.Name == "" {
		c.Name = "Frame"
	}
	if len(c.Name) > 64 {
		errs = append(errs, errors.New("FRAME_NAME must be at most 64 bytes"))
	}

	// FRAME_PUBLIC_URL may list several URLs (the first is canonical and used
	// in invite links). Their hosts, plus FRAME_DOMAIN, are the server's
	// public hosts that Apple/Google nonces must be bound to.
	urls := splitList(str("FRAME_PUBLIC_URL"))
	if len(urls) == 0 && c.Domain != "" {
		urls = []string{"https://" + c.Domain}
	}
	for i, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = append(errs, errors.New("FRAME_PUBLIC_URL must be absolute http(s) URL(s), comma-separated"))
			continue
		}
		if i == 0 {
			c.PublicURL = strings.TrimRight(raw, "/")
		}
		c.addPublicHost(CanonicalHost(u.Scheme, u.Host))
		c.PublicOrigins = append(c.PublicOrigins, u.Scheme+"://"+CanonicalHost(u.Scheme, u.Host))
	}
	if c.Domain != "" {
		c.addPublicHost(c.Domain)
		c.PublicOrigins = append(c.PublicOrigins, "https://"+c.Domain)
	}

	c.Registration = strings.ToLower(str("FRAME_REGISTRATION"))
	switch c.Registration {
	case "open", "invite", "closed":
	default:
		errs = append(errs, errors.New("FRAME_REGISTRATION must be open, invite or closed"))
	}
	ui, err := parseBool(str("FRAME_USER_INVITES"))
	if err != nil {
		errs = append(errs, fmt.Errorf("FRAME_USER_INVITES: %w", err))
	}
	c.UserInvites = ui

	c.Admin = strings.ToLower(str("FRAME_ADMIN"))
	switch c.Admin {
	case "on", "local", "off":
	case "true", "yes", "1":
		c.Admin = "on"
	case "false", "no", "0":
		c.Admin = "off"
	default:
		errs = append(errs, errors.New("FRAME_ADMIN must be on, local or off"))
	}
	c.AdminListen = str("FRAME_ADMIN_LISTEN")
	if c.AdminListen != "" {
		if _, _, err := net.SplitHostPort(c.AdminListen); err != nil {
			errs = append(errs, errors.New("FRAME_ADMIN_LISTEN must be host:port, e.g. 127.0.0.1:8081"))
		}
	}

	for _, m := range splitList(str("FRAME_AUTH_METHODS")) {
		switch strings.ToLower(m) {
		case "email":
			c.AuthEmail = true
		case "phone":
			c.AuthPhone = true
		case "apple":
			c.AuthApple = true
		case "google":
			c.AuthGoogle = true
		default:
			errs = append(errs, fmt.Errorf("FRAME_AUTH_METHODS: unknown method %q", m))
		}
	}
	c.AppleAudiences = splitList(str("FRAME_APPLE_AUDIENCES"))
	if v, ok := src.get("FRAME_GOOGLE_CLIENT_IDS"); ok {
		c.GoogleClientIDs = splitList(v)
	} else {
		c.GoogleClientIDs = splitList(DefaultGoogleClientIDs)
	}

	c.Storage = strings.ToLower(str("FRAME_STORAGE"))
	c.S3Endpoint = strings.TrimRight(str("FRAME_S3_ENDPOINT"), "/")
	c.S3Bucket = str("FRAME_S3_BUCKET")
	c.S3Region = str("FRAME_S3_REGION")
	c.S3AccessKeyID = str("FRAME_S3_ACCESS_KEY_ID")
	c.S3SecretAccessKey = str("FRAME_S3_SECRET_ACCESS_KEY")
	c.S3Prefix = str("FRAME_S3_PREFIX")
	enc, err := parseBool(str("FRAME_S3_ENCRYPT"))
	if err != nil {
		errs = append(errs, fmt.Errorf("FRAME_S3_ENCRYPT: %w", err))
	}
	c.S3Encrypt = enc
	switch c.Storage {
	case "local":
	case "s3":
		if c.S3Endpoint == "" || c.S3Bucket == "" || c.S3AccessKeyID == "" || c.S3SecretAccessKey == "" {
			errs = append(errs, errors.New("FRAME_STORAGE=s3 requires FRAME_S3_ENDPOINT, FRAME_S3_BUCKET, FRAME_S3_ACCESS_KEY_ID and FRAME_S3_SECRET_ACCESS_KEY"))
		} else if err := checkURL(c.S3Endpoint, true); err != nil {
			errs = append(errs, fmt.Errorf("FRAME_S3_ENDPOINT: %w", err))
		}
		if strings.Contains(c.S3Bucket, "/") {
			errs = append(errs, errors.New("FRAME_S3_BUCKET must not contain '/'"))
		}
		if strings.HasPrefix(c.S3Prefix, "/") || strings.Contains(c.S3Prefix, "..") {
			errs = append(errs, errors.New("FRAME_S3_PREFIX must be relative and must not contain '..'"))
		}
	default:
		errs = append(errs, errors.New("FRAME_STORAGE must be local or s3"))
	}

	c.PushRelay = strings.TrimRight(str("FRAME_PUSH_RELAY"), "/")
	if c.PushRelay != "" {
		if err := checkURL(c.PushRelay, true); err != nil {
			errs = append(errs, fmt.Errorf("FRAME_PUSH_RELAY: %w", err))
		}
	}
	c.InviteBase = str("FRAME_INVITE_BASE")
	if err := checkURL(c.InviteBase, false); err != nil {
		errs = append(errs, fmt.Errorf("FRAME_INVITE_BASE: %w", err))
	}

	mb, err := parseInt(str("FRAME_MAX_UPLOAD_MB"), 1, 1024)
	if err != nil {
		errs = append(errs, fmt.Errorf("FRAME_MAX_UPLOAD_MB: %w", err))
	}
	c.MaxUploadBytes = int64(mb) << 20
	secs, err := parseInt(str("FRAME_MAX_VIDEO_SECONDS"), 1, 600)
	if err != nil {
		errs = append(errs, fmt.Errorf("FRAME_MAX_VIDEO_SECONDS: %w", err))
	}
	c.MaxVideoMS = int64(secs) * 1000
	c.MaxFriends, err = parseInt(str("FRAME_MAX_FRIENDS"), 0, 1_000_000)
	if err != nil {
		errs = append(errs, fmt.Errorf("FRAME_MAX_FRIENDS: %w", err))
	}

	for _, p := range splitList(str("FRAME_TRUSTED_PROXIES")) {
		if strings.EqualFold(p, "cloudflare") {
			c.TrustCloudflare = true
			continue
		}
		pref, err := parsePrefix(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("FRAME_TRUSTED_PROXIES: %q is not a CIDR or IP", p))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, pref)
	}

	c.SMTPURL = str("FRAME_SMTP_URL")
	c.SMTPFrom = str("FRAME_SMTP_FROM")
	if c.SMTPURL != "" {
		u, err := url.Parse(c.SMTPURL)
		if err != nil || (u.Scheme != "smtp" && u.Scheme != "smtps") || u.Hostname() == "" {
			errs = append(errs, errors.New("FRAME_SMTP_URL must look like smtps://user:pass@host:465 or smtp://user:pass@host:587"))
		}
		if c.SMTPFrom == "" {
			errs = append(errs, errors.New("FRAME_SMTP_FROM is required when FRAME_SMTP_URL is set"))
		}
	}

	switch strings.ToLower(str("FRAME_LOG_LEVEL")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info", "":
		c.LogLevel = slog.LevelInfo
	case "warn", "warning":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		errs = append(errs, errors.New("FRAME_LOG_LEVEL must be debug, info, warn or error"))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

func (c *Config) addPublicHost(h string) {
	for _, x := range c.PublicHosts {
		if x == h {
			return
		}
	}
	c.PublicHosts = append(c.PublicHosts, h)
}

// CanonicalHost lowercases a host[:port] and drops the scheme's default port
// ("https", "frame.example.com:443" → "frame.example.com").
func CanonicalHost(scheme, hostport string) string {
	h := strings.ToLower(hostport)
	if (scheme == "https" && strings.HasSuffix(h, ":443")) || (scheme == "http" && strings.HasSuffix(h, ":80")) {
		h = h[:strings.LastIndexByte(h, ':')]
	}
	return h
}

// SMTPEnabled reports whether email password reset is available.
func (c *Config) SMTPEnabled() bool { return c.SMTPURL != "" }

// AppleEnabled reports whether Sign in with Apple is usable.
func (c *Config) AppleEnabled() bool { return c.AuthApple && len(c.AppleAudiences) > 0 }

// GoogleEnabled reports whether Google sign-in is usable.
func (c *Config) GoogleEnabled() bool { return c.AuthGoogle && len(c.GoogleClientIDs) > 0 }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off", "":
		return false, nil
	}
	return false, errors.New("must be true or false")
}

func parseInt(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.New("must be an integer")
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("must be between %d and %d", lo, hi)
	}
	return n, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// checkURL validates an absolute URL. When httpsOnly is set, plain http is
// allowed only for loopback hosts (local testing).
func checkURL(s string, httpsOnly bool) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return errors.New("must be an absolute URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if !httpsOnly {
			return nil
		}
		h := u.Hostname()
		if h == "localhost" {
			return nil
		}
		if a, err := netip.ParseAddr(h); err == nil && a.IsLoopback() {
			return nil
		}
		return errors.New("must use https")
	}
	return errors.New("must be an http(s) URL")
}
