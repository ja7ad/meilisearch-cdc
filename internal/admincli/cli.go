// Package admincli is `meilisearch-cdc admin`: operator commands run on the
// server, through the running service's admin socket or, while the service
// is stopped, directly on its data directory.
package admincli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/adminops"
	"github.com/ja7ad/meilisearch-cdc/internal/app"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// IO is where the CLI reads and writes.
type IO struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// flags are the union of every command's flags.
type flags struct {
	email, name, role, passwordFile string
	generate, yes, indexes, json    bool
}

type command struct {
	path    string // e.g. "users reset-password"
	args    string // positional arguments in the synopsis
	summary string
	help    string
	setup   func(fs *flag.FlagSet, f *flags)
	request func(c *cli, f *flags, args []string) (adminops.Request, error)
	print   func(c *cli, res adminops.Result)
}

func emailFlag(fs *flag.FlagSet, f *flags) {
	fs.StringVar(&f.email, "email", "", "the account's email address (required)")
}

func passwordFlags(fs *flag.FlagSet, f *flags) {
	fs.StringVar(&f.passwordFile, "password-file", "", "read the password from this file (- for stdin)")
	fs.BoolVar(&f.generate, "generate", false, "generate a strong random password and print it once")
}

const passwordHelp = `The password comes from --password-file, --generate, or a prompt (asked
twice, not echoed) when stdin is a terminal; otherwise the first line of
stdin is used. Passwords have 8 to 128 characters.`

var commands = []command{
	{
		path: "users list", summary: "list accounts",
		request: func(*cli, *flags, []string) (adminops.Request, error) {
			return adminops.Request{Command: adminops.UsersList}, nil
		},
		print: printUsers,
	},
	{
		path: "users create", summary: "add an account",
		help: "Creates an account. The first admin of an empty installation becomes the\nprimary administrator.\n\n" + passwordHelp,
		setup: func(fs *flag.FlagSet, f *flags) {
			emailFlag(fs, f)
			fs.StringVar(&f.name, "name", "", "display name (default: the part of the email before @)")
			fs.StringVar(&f.role, "role", "viewer", "viewer, moderator or admin")
			passwordFlags(fs, f)
		},
		request: func(c *cli, f *flags, _ []string) (adminops.Request, error) {
			if f.email == "" {
				return adminops.Request{}, usageError("--email is required")
			}
			pw, err := c.password(f)
			return adminops.Request{Command: adminops.UsersCreate, Email: f.email, Name: f.name, Role: f.role, Password: pw}, err
		},
	},
	{
		path: "users reset-password", summary: "set a new password and sign the user out everywhere",
		help:  "Works for every account, the primary administrator included.\n\n" + passwordHelp,
		setup: func(fs *flag.FlagSet, f *flags) { emailFlag(fs, f); passwordFlags(fs, f) },
		request: func(c *cli, f *flags, _ []string) (adminops.Request, error) {
			if f.email == "" {
				return adminops.Request{}, usageError("--email is required")
			}
			pw, err := c.password(f)
			return adminops.Request{Command: adminops.UsersResetPassword, Email: f.email, Password: pw}, err
		},
	},
	{
		path: "users set-role", summary: "change an account's role",
		help: "The primary administrator's role cannot change: transfer ownership with\n`users set-owner` first. The last admin cannot be demoted.",
		setup: func(fs *flag.FlagSet, f *flags) {
			emailFlag(fs, f)
			fs.StringVar(&f.role, "role", "", "viewer, moderator or admin (required)")
		},
		request: func(_ *cli, f *flags, _ []string) (adminops.Request, error) {
			if f.email == "" || f.role == "" {
				return adminops.Request{}, usageError("--email and --role are required")
			}
			return adminops.Request{Command: adminops.UsersSetRole, Email: f.email, Role: f.role}, nil
		},
	},
	{
		path: "users set-owner", summary: "make an admin the primary administrator",
		help:  "The primary administrator's role and account cannot be changed or deleted\nfrom the dashboard or the API. Only this command transfers it, to an admin.",
		setup: func(fs *flag.FlagSet, f *flags) { emailFlag(fs, f) },
		request: func(_ *cli, f *flags, _ []string) (adminops.Request, error) {
			if f.email == "" {
				return adminops.Request{}, usageError("--email is required")
			}
			return adminops.Request{Command: adminops.UsersSetOwner, Email: f.email}, nil
		},
	},
	{
		path: "users delete", summary: "delete an account and its sessions",
		help:  "The primary administrator and the last admin cannot be deleted.",
		setup: func(fs *flag.FlagSet, f *flags) { emailFlag(fs, f) },
		request: func(_ *cli, f *flags, _ []string) (adminops.Request, error) {
			if f.email == "" {
				return adminops.Request{}, usageError("--email is required")
			}
			return adminops.Request{Command: adminops.UsersDelete, Email: f.email}, nil
		},
	},
	{
		path: "sessions revoke", summary: "sign one user, or everyone, out",
		setup: func(fs *flag.FlagSet, f *flags) {
			fs.StringVar(&f.email, "email", "", "sign out only this account (default: everyone)")
		},
		request: func(_ *cli, f *flags, _ []string) (adminops.Request, error) {
			return adminops.Request{Command: adminops.SessionsRevoke, Email: f.email}, nil
		},
	},
	{
		path: "mcp disable", summary: "turn the MCP server off (tokens and policy stay)",
		request: func(*cli, *flags, []string) (adminops.Request, error) {
			return adminops.Request{Command: adminops.MCPDisable}, nil
		},
	},
	{
		path: "mcp revoke-tokens", summary: "revoke every MCP token",
		request: func(*cli, *flags, []string) (adminops.Request, error) {
			return adminops.Request{Command: adminops.MCPRevokeTokens}, nil
		},
	},
	{
		path: "purge", args: "<scope>...", summary: "permanently delete stored data",
		help: purgeHelp(),
		setup: func(fs *flag.FlagSet, f *flags) {
			fs.BoolVar(&f.yes, "yes", false, "do not ask for confirmation")
			fs.BoolVar(&f.indexes, "indexes", false, "with the tasks scope, also delete each task's Meilisearch index")
		},
		request: func(c *cli, f *flags, args []string) (adminops.Request, error) {
			scopes, err := adminops.ParseScopes(args)
			if err != nil {
				return adminops.Request{}, usageError(err.Error())
			}
			if !f.yes {
				if err := c.confirmPurge(scopes); err != nil {
					return adminops.Request{}, err
				}
			}
			return adminops.Request{Command: adminops.Purge, Scopes: args, Indexes: f.indexes}, nil
		},
		print: printPurged,
	},
}

func purgeHelp() string {
	var b strings.Builder
	b.WriteString("Deletes exactly the data of each scope, in this order:\n\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, s := range adminops.Scopes {
		fmt.Fprintf(tw, "  %s\t%s\n", s.Name, s.Description)
	}
	fmt.Fprintf(tw, "  %s\t%s\n", adminops.All, "everything: as if the data directory were new (the secret key file stays)")
	tw.Flush()
	b.WriteString("\nIt asks you to type \"purge\" unless --yes is given. While the service runs,\nthe purge goes through it, so affected tasks stop cleanly.")
	return b.String()
}

type usageError string

func (e usageError) Error() string { return string(e) }

type cli struct {
	cfg       config.Config
	io        IO
	json      bool
	generated string // a --generate password, printed once after success
	stdin     *bufio.Reader
}

// Run executes `meilisearch-cdc admin` with args (after "admin") and
// returns the exit code.
func Run(ctx context.Context, args []string, stdio IO) int {
	c := &cli{cfg: config.FromEnv(), io: stdio, stdin: bufio.NewReader(stdio.Stdin)}
	root := flag.NewFlagSet("meilisearch-cdc admin", flag.ContinueOnError)
	root.SetOutput(stdio.Stderr)
	c.cfg.Flags(root)
	root.BoolVar(&c.json, "json", false, "print machine readable JSON")
	root.Usage = func() {} // printed below, to stdout for --help
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.usage(stdio.Stdout)
			return exitOK
		}
		c.usage(stdio.Stderr)
		return exitUsage
	}
	rest := root.Args()
	if len(rest) == 0 {
		c.usage(stdio.Stderr)
		return exitUsage
	}
	if rest[0] == "help" {
		if len(rest) == 1 {
			c.usage(stdio.Stdout)
			return exitOK
		}
		rest = append(rest[1:], "-help")
	}
	cmd, n := find(rest)
	if cmd == nil {
		return c.groupUsage(rest)
	}
	var f flags
	fs := flag.NewFlagSet("meilisearch-cdc admin "+cmd.path, flag.ContinueOnError)
	fs.SetOutput(stdio.Stderr)
	if cmd.setup != nil {
		cmd.setup(fs, &f)
	}
	fs.BoolVar(&f.json, "json", c.json, "print machine readable JSON")
	fs.Usage = func() {}
	positional, err := parseInterleaved(fs, rest[n:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			commandUsage(stdio.Stdout, cmd, fs)
			return exitOK
		}
		commandUsage(stdio.Stderr, cmd, fs)
		return exitUsage
	}
	if cmd.args == "" && len(positional) > 0 {
		fmt.Fprintf(stdio.Stderr, "unexpected argument %q\n", positional[0])
		commandUsage(stdio.Stderr, cmd, fs)
		return exitUsage
	}
	c.json = f.json
	req, err := cmd.request(c, &f, positional)
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stdio.Stderr, "error: %s\nRun \"meilisearch-cdc admin %s --help\" for usage.\n", err, cmd.path)
			return exitUsage
		}
		fmt.Fprintf(stdio.Stderr, "error: %s\n", err)
		return exitError
	}
	res, err := c.exec(ctx, req)
	if err != nil {
		if len(res.Purged) > 0 && !c.json {
			printPurged(c, res)
		}
		fmt.Fprintf(stdio.Stderr, "error: %s\n", err)
		return exitError
	}
	if c.json {
		var out any = struct {
			adminops.Result
			Password string `json:"password,omitempty"`
		}{res, c.generated}
		if req.Command == adminops.UsersList {
			out = map[string]any{"users": append([]auth.PublicUser{}, res.Users...)}
		}
		enc := json.NewEncoder(stdio.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return exitOK
	}
	if cmd.print != nil {
		cmd.print(c, res)
	} else {
		fmt.Fprintln(stdio.Stdout, res.Message)
	}
	if c.generated != "" {
		fmt.Fprintf(stdio.Stdout, "Generated password (shown only once): %s\n", c.generated)
	}
	return exitOK
}

// find returns the command named by the leading words of args and how many
// words it used.
func find(args []string) (*command, int) {
	for i := range commands {
		words := strings.Fields(commands[i].path)
		if len(args) >= len(words) && strings.Join(args[:len(words)], " ") == commands[i].path {
			return &commands[i], len(words)
		}
	}
	return nil, 0
}

// parseInterleaved parses flags that may come after positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		if args[0] == "--" {
			return append(positional, args[1:]...), nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func (c *cli) usage(w io.Writer) {
	fmt.Fprint(w, `Usage: meilisearch-cdc admin [config flags] <command> [flags]

Operator commands, run on the server. They go through the running service
(its admin socket <data-dir>/admin.sock) or, while it is stopped, work on
the data directory directly. Every command is recorded in the audit log
with the actor "cli".

Commands:
`)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, cmd := range commands {
		fmt.Fprintf(tw, "  %s\t%s\n", strings.TrimSpace(cmd.path+" "+cmd.args), cmd.summary)
	}
	tw.Flush()
	fmt.Fprint(w, `
Config flags are the service's own, with the same environment variables, so
the CLI finds the same data directory and key: -data-dir (CDC_DATA_DIR),
-secret-key (CDC_SECRET_KEY), -meili-url (MEILI_URL), ...
  -json    print machine readable JSON

Run "meilisearch-cdc admin <command> --help" for a command's flags.
`)
}

// groupUsage handles "admin users" and unknown commands.
func (c *cli) groupUsage(args []string) int {
	var sub []command
	for _, cmd := range commands {
		if strings.HasPrefix(cmd.path, args[0]+" ") {
			sub = append(sub, cmd)
		}
	}
	if len(sub) == 0 {
		fmt.Fprintf(c.io.Stderr, "unknown command %q\n\n", args[0])
		c.usage(c.io.Stderr)
		return exitUsage
	}
	help := len(args) > 1 && (args[1] == "-h" || args[1] == "-help" || args[1] == "--help")
	w, code := c.io.Stderr, exitUsage
	if help {
		w, code = c.io.Stdout, exitOK
	} else if len(args) > 1 {
		fmt.Fprintf(w, "unknown command %q\n\n", args[0]+" "+args[1])
	}
	fmt.Fprintf(w, "Usage: meilisearch-cdc admin %s <command> [flags]\n\nCommands:\n", args[0])
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, cmd := range sub {
		fmt.Fprintf(tw, "  %s\t%s\n", strings.TrimSpace(cmd.path+" "+cmd.args), cmd.summary)
	}
	tw.Flush()
	return code
}

func commandUsage(w io.Writer, cmd *command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage: meilisearch-cdc admin %s [flags]\n\n%s.\n", strings.TrimSpace(cmd.path+" "+cmd.args), upperFirst(cmd.summary))
	if cmd.help != "" {
		fmt.Fprintf(w, "\n%s\n", cmd.help)
	}
	fmt.Fprint(w, "\nFlags:\n")
	fs.SetOutput(w)
	fs.PrintDefaults()
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// exec runs req through the service's admin socket, or on the data
// directory when the service is stopped.
func (c *cli) exec(ctx context.Context, req adminops.Request) (adminops.Result, error) {
	// Signals interrupt prompts the default way; from here on they stop
	// the command cleanly (the store is closed).
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	sock := app.SocketPath(c.cfg)
	res, err := adminops.Call(ctx, sock, req)
	if !errors.Is(err, adminops.ErrNoServer) {
		return res, err
	}
	if _, err := os.Stat(c.cfg.DBPath()); err != nil {
		return adminops.Result{}, fmt.Errorf("no database at %s: check -data-dir (CDC_DATA_DIR)", c.cfg.DBPath())
	}
	log := slog.New(slog.NewTextHandler(c.io.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	a, err := app.OpenOffline(c.cfg, log)
	if errors.Is(err, store.ErrLocked) {
		return adminops.Result{}, fmt.Errorf("%s is locked by a running service that does not answer on %s (an older version, or the socket could not be created; see its log): stop the service and retry", c.cfg.DBPath(), sock)
	}
	if err != nil {
		return adminops.Result{}, err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), c.cfg.StopTimeout+10*time.Second)
		defer cancel()
		_ = a.Close(cctx)
	}()
	return a.Ops.Do(ctx, req)
}

// password returns the password chosen by the flags, a prompt or stdin.
func (c *cli) password(f *flags) (string, error) {
	switch {
	case f.passwordFile != "" && f.generate:
		return "", usageError("use either --password-file or --generate")
	case f.generate:
		c.generated = adminops.GeneratePassword()
		return c.generated, nil
	case f.passwordFile == "-":
		raw, err := io.ReadAll(c.stdin)
		return strings.TrimRight(string(raw), "\r\n"), err
	case f.passwordFile != "":
		raw, err := os.ReadFile(f.passwordFile)
		return strings.TrimRight(string(raw), "\r\n"), err
	}
	if fd, ok := c.terminal(); ok {
		fmt.Fprint(c.io.Stderr, "New password: ")
		first, err := readNoEcho(fd)
		fmt.Fprintln(c.io.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(c.io.Stderr, "Repeat it: ")
		second, err := readNoEcho(fd)
		fmt.Fprintln(c.io.Stderr)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("the passwords do not match")
		}
		return string(first), nil
	}
	line, err := c.stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if line = strings.TrimRight(line, "\r\n"); line == "" {
		return "", errors.New("no password given: use --password-file, --generate or write it to stdin")
	}
	return line, nil
}

// terminal returns stdin's descriptor when it is a terminal.
func (c *cli) terminal() (int, bool) {
	f, ok := c.io.Stdin.(*os.File)
	if !ok || !isTerminal(int(f.Fd())) {
		return 0, false
	}
	return int(f.Fd()), true
}

func (c *cli) confirmPurge(scopes []string) error {
	w := c.io.Stderr
	fmt.Fprintf(w, "This permanently deletes from %s:\n", c.cfg.DataDir)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range scopes {
		for _, s := range adminops.Scopes {
			if s.Name == name {
				fmt.Fprintf(tw, "  %s\t%s\n", s.Name, s.Description)
			}
		}
	}
	tw.Flush()
	fmt.Fprint(w, `Type "purge" to confirm: `)
	line, err := c.stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if _, tty := c.terminal(); !tty {
		fmt.Fprintln(w) // a terminal echoed the newline already
	}
	if strings.TrimSpace(line) != "purge" {
		return errors.New("purge cancelled: nothing was deleted")
	}
	return nil
}

func when(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func printUsers(c *cli, res adminops.Result) {
	if len(res.Users) == 0 {
		fmt.Fprintln(c.io.Stdout, "No users: the dashboard shows the setup page.")
		return
	}
	tw := tabwriter.NewWriter(c.io.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EMAIL\tNAME\tROLE\tOWNER\tLAST SIGN-IN\tCREATED")
	for _, u := range res.Users {
		owner := ""
		if u.Owner {
			owner = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", u.Email, u.Name, u.Role, owner, when(u.LastLoginAt), when(u.CreatedAt))
	}
	tw.Flush()
}

func printPurged(c *cli, res adminops.Result) {
	tw := tabwriter.NewWriter(c.io.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCOPE\tREMOVED")
	for _, p := range res.Purged {
		fmt.Fprintf(tw, "%s\t%d\n", p.Scope, p.Count)
	}
	tw.Flush()
}
