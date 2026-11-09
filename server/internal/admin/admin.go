// Package admin implements the `frame-server admin …` CLI commands. They
// operate directly on the data directory and are safe to run while the
// server is running (SQLite WAL + busy timeout).
package admin

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/api"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/auth"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/db"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/ids"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/store"
)

// Env is what admin commands need.
type Env struct {
	DB         *db.DB
	API        *api.Server
	PublicURL  string // FRAME_PUBLIC_URL (may be empty)
	InviteBase string
	AdminURL   string // where the web admin panel is reached (setup links)
	AdminOff   bool   // FRAME_ADMIN=off
	Out        io.Writer
}

// Usage is printed for unknown admin commands.
const Usage = `usage:
  frame-server admin setup-link
  frame-server admin user list
  frame-server admin user create --email E | --phone P [--name N] [--username U] [--admin]
  frame-server admin user promote <username>
  frame-server admin user demote <username>
  frame-server admin user reset-2fa <username>
  frame-server admin user reset-password <username|email|phone>
  frame-server admin user unlink <username|email|phone> apple|google
  frame-server admin user delete <username>
  frame-server admin invite create <username>
  frame-server admin invite server [--uses N] [--days D] [--note TEXT]`

// Run executes an admin command (args after "admin").
func Run(ctx context.Context, env *Env, args []string) error {
	if len(args) == 1 && args[0] == "setup-link" {
		return setupLink(ctx, env)
	}
	if len(args) < 2 {
		return errors.New(Usage)
	}
	one := func(name string, fn func(context.Context, *Env, string) error) error {
		if len(args) != 3 {
			return fmt.Errorf("usage: frame-server admin %s %s <username>", args[0], name)
		}
		return fn(ctx, env, args[2])
	}
	switch args[0] + " " + args[1] {
	case "user list":
		return userList(ctx, env)
	case "user create":
		return userCreate(ctx, env, args[2:])
	case "user promote":
		return one("promote", func(ctx context.Context, env *Env, who string) error { return setAdmin(ctx, env, who, true) })
	case "user demote":
		return one("demote", func(ctx context.Context, env *Env, who string) error { return setAdmin(ctx, env, who, false) })
	case "user reset-2fa":
		return one("reset-2fa", reset2FA)
	case "invite server":
		return serverInvite(ctx, env, args[2:])
	case "user reset-password":
		if len(args) != 3 {
			return errors.New("usage: frame-server admin user reset-password <username|email|phone>")
		}
		return resetPassword(ctx, env, args[2])
	case "user unlink":
		if len(args) != 4 {
			return errors.New("usage: frame-server admin user unlink <username|email|phone> apple|google")
		}
		return unlink(ctx, env, args[2], args[3])
	case "user delete":
		if len(args) != 3 {
			return errors.New("usage: frame-server admin user delete <username>")
		}
		return userDelete(ctx, env, args[2])
	case "invite create":
		if len(args) != 3 {
			return errors.New("usage: frame-server admin invite create <username>")
		}
		return inviteCreate(ctx, env, args[2])
	}
	return errors.New(Usage)
}

func deref(p *string) string {
	if p == nil {
		return "-"
	}
	return *p
}

func userList(ctx context.Context, env *Env) error {
	users, err := store.ListUsers(ctx, env.DB.R)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(env.Out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tUSERNAME\tNAME\tEMAIL\tPHONE\tMETHODS\tADMIN\tSUSPENDED\tCREATED")
	for _, u := range users {
		var m []string
		if u.PasswordHash != nil {
			m = append(m, "password")
		}
		if u.AppleSub != nil {
			m = append(m, "apple")
		}
		if u.GoogleSub != nil {
			m = append(m, "google")
		}
		admin, suspended := "-", "-"
		if u.IsAdmin {
			admin = "yes"
		}
		if u.DisabledAt != nil {
			suspended = time.UnixMilli(*u.DisabledAt).UTC().Format("2006-01-02")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", u.ID, deref(u.Username), u.DisplayName, deref(u.Email), deref(u.Phone),
			strings.Join(m, ","), admin, suspended, time.UnixMilli(u.CreatedAt).UTC().Format("2006-01-02 15:04"))
	}
	return tw.Flush()
}

// findUser resolves a username, email or phone.
func findUser(ctx context.Context, q store.Q, who string) (*store.User, error) {
	who = strings.TrimSpace(who)
	var col, val string
	switch {
	case strings.HasPrefix(who, "+"):
		v, ok := auth.NormalizePhone(who)
		if !ok {
			return nil, errors.New("invalid phone number")
		}
		col, val = "phone", v
	case strings.Contains(who, "@") && !strings.HasPrefix(who, "@"):
		v, ok := auth.NormalizeEmail(who)
		if !ok {
			return nil, errors.New("invalid email")
		}
		col, val = "email", v
	default:
		col, val = "username", strings.ToLower(strings.TrimPrefix(who, "@"))
	}
	u, err := store.UserBy(ctx, q, col, val)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no user with %s %q", col, val)
	}
	return u, err
}

func userCreate(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	email := fs.String("email", "", "email address")
	phone := fs.String("phone", "", "E.164 phone number")
	name := fs.String("name", "", "display name")
	username := fs.String("username", "", "username")
	makeAdmin := fs.Bool("admin", false, "also make the user an admin of the web panel")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\nusage: frame-server admin user create --email E | --phone P [--name N] [--username U] [--admin]", err)
	}
	if (*email == "") == (*phone == "") {
		return errors.New("exactly one of --email or --phone is required")
	}
	now := time.Now().UnixMilli()
	u := &store.User{ID: ids.New(), Discoverable: true, Searchable: true, ReadReceipts: true, CreatedAt: now, UpdatedAt: now}
	if *email != "" {
		v, ok := auth.NormalizeEmail(*email)
		if !ok {
			return errors.New("invalid email")
		}
		u.Email = &v
		u.DisplayName, _, _ = strings.Cut(v, "@")
	} else {
		v, ok := auth.NormalizePhone(*phone)
		if !ok {
			return errors.New("phone must be E.164, e.g. +34600111222")
		}
		u.Phone = &v
		u.DisplayName = "Frame user"
	}
	if *name != "" {
		n, ok := auth.CleanDisplayName(*name)
		if !ok {
			return errors.New("name must be 1–40 characters")
		}
		u.DisplayName = n
	} else if n, ok := auth.CleanDisplayName(u.DisplayName); ok {
		u.DisplayName = n
	} else {
		u.DisplayName = "Frame user"
	}
	if *username != "" {
		un := strings.ToLower(*username)
		if msg := auth.UsernameError(un); msg != "" {
			return errors.New("username: " + msg)
		}
		u.Username = &un
	}
	pw := auth.RandomPassword()
	hash, err := auth.HashPassword(ctx, pw)
	if err != nil {
		return err
	}
	u.PasswordHash = &hash
	u.IsAdmin = *makeAdmin
	err = env.DB.Tx(ctx, func(tx *sql.Tx) error { return store.InsertUser(ctx, tx, u) })
	switch {
	case errors.Is(err, store.ErrIdentifierTaken):
		return errors.New("a user with this email/phone already exists")
	case errors.Is(err, store.ErrUsernameTaken):
		return errors.New("username is taken")
	case err != nil:
		return err
	}
	detail := ""
	if u.IsAdmin {
		detail = "as admin"
	}
	env.API.AuditCLI(ctx, "user.create", api.UserTarget(u), detail)
	fmt.Fprintf(env.Out, "created user %s (%s)%s\none-time password: %s\n", u.ID, u.DisplayName, map[bool]string{true: " as admin", false: ""}[u.IsAdmin], pw)
	return nil
}

func setupLink(ctx context.Context, env *Env) error {
	if env.AdminOff {
		return errors.New("the web admin panel is off (FRAME_ADMIN=off); create an admin with `frame-server admin user create --email E --admin`")
	}
	tok, exp, err := env.API.NewSetupToken(ctx)
	if errors.Is(err, api.ErrSetupDone) {
		return err
	}
	if err != nil {
		return err
	}
	env.API.AuditCLI(ctx, "setup_link.create", nil, "")
	fmt.Fprintf(env.Out, "Create your admin account: %s/admin/setup#t=%s\n(works once, valid until %s; replaces any earlier link)\n",
		strings.TrimRight(env.AdminURL, "/"), tok, exp.UTC().Format("2006-01-02 15:04 MST"))
	return nil
}

func setAdmin(ctx context.Context, env *Env, who string, admin bool) error {
	u, err := findUser(ctx, env.DB.R, who)
	if err != nil {
		return err
	}
	if u.IsAdmin == admin {
		fmt.Fprintf(env.Out, "%s is already %s\n", u.DisplayName, map[bool]string{true: "an admin", false: "not an admin"}[admin])
		return nil
	}
	if err := env.API.SetAdmin(ctx, u.ID, admin); err != nil {
		if ae := (*api.Error)(nil); errors.As(err, &ae) && ae.Code == "last_admin" {
			return errors.New("refusing to remove the last admin; promote someone else first")
		}
		return err
	}
	action := map[bool]string{true: "user.promote", false: "user.demote"}[admin]
	env.API.AuditCLI(ctx, action, api.UserTarget(u), "")
	fmt.Fprintf(env.Out, "%s (%s) is %s\n", u.DisplayName, u.ID, map[bool]string{true: "now an admin", false: "no longer an admin"}[admin])
	return nil
}

func reset2FA(ctx context.Context, env *Env, who string) error {
	u, err := findUser(ctx, env.DB.R, who)
	if err != nil {
		return err
	}
	if err := env.DB.Tx(ctx, func(tx *sql.Tx) error { return store.DeleteTOTP(ctx, tx, u.ID) }); err != nil {
		return err
	}
	env.API.AuditCLI(ctx, "totp.reset", api.UserTarget(u), "")
	fmt.Fprintf(env.Out, "two-factor authentication turned off for %s; they can sign in with their password and set it up again\n", u.DisplayName)
	return nil
}

func serverInvite(ctx context.Context, env *Env, args []string) error {
	fs := flag.NewFlagSet("invite server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	uses := fs.Int("uses", 1, "how many people can join with it (1–1000)")
	days := fs.Int("days", 7, "days until it expires (1–365; 0 = never)")
	note := fs.String("note", "", "a note for yourself, e.g. who it's for")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\nusage: frame-server admin invite server [--uses N] [--days D] [--note TEXT]", err)
	}
	if *days < 0 || *days > 365 {
		return errors.New("--days must be between 0 (never) and 365")
	}
	var exp *time.Time
	if *days > 0 {
		t := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
		exp = &t
	}
	inv, code, err := env.API.CreateServerInvite(ctx, nil, *uses, exp, *note)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			return errors.New(ae.Message)
		}
		return err
	}
	env.API.AuditCLI(ctx, "invite.create", &api.AuditTarget{Type: "invite", ID: inv.ID, Label: inv.Note}, fmt.Sprintf("%d use(s) via CLI", *uses))
	when := "never expires"
	if exp != nil {
		when = "expires " + exp.UTC().Format(time.RFC3339)
	}
	if env.PublicURL == "" {
		fmt.Fprintf(env.Out, "server invite code: %s (%d use(s), %s)\nset FRAME_PUBLIC_URL (or FRAME_DOMAIN) to print a shareable link\n", code, *uses, when)
		return nil
	}
	fmt.Fprintf(env.Out, "%s\n(%d use(s), %s)\n", api.InviteURL(env.InviteBase, env.PublicURL, code), *uses, when)
	return nil
}

func resetPassword(ctx context.Context, env *Env, who string) error {
	u, err := findUser(ctx, env.DB.R, who)
	if err != nil {
		return err
	}
	if u.Email == nil && u.Phone == nil {
		return errors.New("this account signs in with Apple/Google only and has no email/phone to log in with")
	}
	pw := auth.RandomPassword()
	hash, err := auth.HashPassword(ctx, pw)
	if err != nil {
		return err
	}
	var revoked []string
	err = env.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetPassword(ctx, tx, u.ID, hash, time.Now().UnixMilli()); err != nil {
			return err
		}
		revoked, err = store.DeleteUserSessions(ctx, tx, u.ID, "")
		return err
	})
	if err != nil {
		return err
	}
	_ = store.DeleteAdminSessions(ctx, env.DB.W, u.ID, "")
	env.API.AuditCLI(ctx, "user.reset_password", api.UserTarget(u), "")
	fmt.Fprintf(env.Out, "password reset for %s (%d sessions signed out)\none-time password: %s\n", u.DisplayName, len(revoked), pw)
	return nil
}

// unlink removes an Apple or Google sign-in from an account and signs out
// all its devices: the way back for a member whose session was used to link
// someone else's identity (revoking sessions or changing the password
// doesn't remove a linked identity).
func unlink(ctx context.Context, env *Env, who, provider string) error {
	if provider != "apple" && provider != "google" {
		return errors.New(`provider must be "apple" or "google"`)
	}
	u, err := findUser(ctx, env.DB.R, who)
	if err != nil {
		return err
	}
	other := u.GoogleSub
	if provider == "google" {
		other = u.AppleSub
	}
	if u.PasswordHash == nil && other == nil {
		return fmt.Errorf("%s is the only way %s signs in; refusing (delete the account instead, or link another method first)", provider, u.DisplayName)
	}
	var revoked []string
	err = env.DB.Tx(ctx, func(tx *sql.Tx) error {
		ok, err := store.UnlinkSub(ctx, tx, u.ID, provider, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s has no %s sign-in linked", u.DisplayName, provider)
		}
		revoked, err = store.DeleteUserSessions(ctx, tx, u.ID, "")
		return err
	})
	if err != nil {
		return err
	}
	env.API.AuditCLI(ctx, "user.unlink", api.UserTarget(u), provider)
	fmt.Fprintf(env.Out, "removed %s sign-in from %s (%d sessions signed out)\n", provider, u.DisplayName, len(revoked))
	return nil
}

func userDelete(ctx context.Context, env *Env, who string) error {
	u, err := findUser(ctx, env.DB.R, who)
	if err != nil {
		return err
	}
	if u.IsAdmin && u.DisabledAt == nil {
		if n, err := store.CountActiveAdmins(ctx, env.DB.R); err != nil {
			return err
		} else if n <= 1 {
			return errors.New("refusing to delete the last admin; promote someone else first")
		}
	}
	if err := env.API.DeleteAccount(ctx, u.ID); err != nil {
		return err
	}
	env.API.AuditCLI(ctx, "user.delete", api.UserTarget(u), "")
	fmt.Fprintf(env.Out, "deleted user %s (%s) and all their data\n", u.ID, u.DisplayName)
	return nil
}

func inviteCreate(ctx context.Context, env *Env, who string) error {
	u, err := findUser(ctx, env.DB.R, who)
	if err != nil {
		return err
	}
	code, expires, err := env.API.CreateInvite(ctx, u.ID)
	if err != nil {
		return err
	}
	exp := time.UnixMilli(expires).UTC().Format(time.RFC3339)
	if env.PublicURL == "" {
		fmt.Fprintf(env.Out, "invite code: %s (expires %s)\nset FRAME_PUBLIC_URL (or FRAME_DOMAIN) to print a shareable link\n", code, exp)
		return nil
	}
	fmt.Fprintf(env.Out, "%s\n(expires %s)\n", api.InviteURL(env.InviteBase, env.PublicURL, code), exp)
	return nil
}
