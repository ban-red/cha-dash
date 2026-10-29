package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/config"
	"dash.cha.sh/internal/store"
)

// bansCmd lists and clears addresses banned after too many wrong passwords:
//
//	chadash bans               list them
//	chadash bans clear <ip>    lift one ban
//
// It works on the database directly, whether or not the hub is running, so
// it's the way back in when every admin's address is banned. A running hub
// reads bans on every sign-in, so a cleared ban applies at once.
func bansCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	clearing := len(args) > 0 && args[0] == "clear"
	name := "chadash bans"
	if clearing {
		name, args = "chadash bans clear", args[1:]
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	loader := config.Bind(fs, getenv)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: chadash bans [flags]
       chadash bans clear [flags] <ip>

Lists the client addresses banned after %d wrong passwords in a row, or
clears one. A banned address can't sign in until its ban is cleared, here
or in Settings → Users. Works whether or not the hub is running.

Flags:
`, auth.BanAfter)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if clearing != (fs.NArg() == 1) || fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	cfg, err := loader.Config()
	if err != nil {
		fmt.Fprintf(stderr, "chadash bans: %v\n", err)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "chadash bans: %v\n", err)
		return 1
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return fail(fmt.Errorf("there's no hub database at %s (set --data-dir or CHADASH_DATA_DIR)", cfg.DBPath()))
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return fail(err)
	}

	if clearing {
		a, err := auth.Unban(ctx, st, fs.Arg(0))
		var verr *auth.ValidationError
		switch {
		case errors.As(err, &verr):
			fmt.Fprintf(stderr, "chadash bans: %q isn't an IP address\n", fs.Arg(0))
			return 2
		case errors.Is(err, auth.ErrBanNotFound):
			return fail(fmt.Errorf("%s isn't banned", fs.Arg(0)))
		case err != nil:
			return fail(err)
		}
		fmt.Fprintf(stdout, "Cleared the ban on %s. It can sign in again.\n", a.Unmap())
		return 0
	}
	bans, err := auth.ListBans(ctx, st)
	if err != nil {
		return fail(err)
	}
	if len(bans) == 0 {
		fmt.Fprintln(stdout, "No addresses are banned.")
		return 0
	}
	for _, b := range bans {
		fmt.Fprintf(stdout, "%s\tbanned %s after %d wrong passwords\n", b.IP, b.BannedAt.Format("2006-01-02 15:04 MST"), b.Failures)
	}
	fmt.Fprintln(stdout, "\nClear one with: chadash bans clear <ip>")
	return 0
}
