package vault

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/fclaude/rauli/internal/tools"
)

const usage = `Usage: rauli keychain COMMAND

Commands:
  add [--origin ORIGIN]... [--anywhere] [--replace] NAME FIELD...
        Store a login. Name its fields, such as user and password; the values
        are asked for one by one, with the secret ones hidden. Without a terminal
        they are read from standard input, one per line, in the order given.
        A login needs the origins where it may be used, such as
        --origin https://mail.example.com. Pass --anywhere to allow every site.
        A field called totp holds the seed of a one-time code, and the code is
        what gets typed.
  list                 Show the logins: names, fields and origins, never values.
  show NAME            Show one login, never values.
  remove NAME          Delete a login.
  test NAME URL        Check that a login can be used on a page and that its
                       one-time code seed works.

Put options before the name.

With the command backend the logins are the [[keychain.handles]] tables of the
configuration and their values stay in your password manager, so only list, show
and test work. Test runs your program for each field, shows none of the values,
and lets its standard error through so you can see why it failed.
`

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ", ") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// Run is the keychain's command line. args are what follows "rauli keychain".
// Secrets are read from in, and nothing it prints holds a value.
func Run(ctx context.Context, cfg Config, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, usage)
		if len(args) == 0 {
			return errors.New("say what to do: add, list, show, remove or test")
		}
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "add", "list", "show", "remove", "test":
	default:
		return fmt.Errorf("%q is not a keychain command: use add, list, show, remove or test", cmd)
	}

	// Nothing is read from standard input for a keychain that cannot take it.
	if cfg.Backend == "command" && (cmd == "add" || cmd == "remove") {
		return errNotHere
	}

	// The flags of add are read before the keychain is opened, so a mistake in
	// them is reported without asking for a passphrase.
	var add AddOptions
	var origins repeated
	var fields []string
	if cmd == "add" {
		fs := flag.NewFlagSet("keychain add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.Var(&origins, "origin", "")
		fs.BoolVar(&add.Anywhere, "anywhere", false, "")
		fs.BoolVar(&add.Replace, "replace", false, "")
		if err := fs.Parse(rest); err != nil {
			return fmt.Errorf("%v\n\n%s", err, usage)
		}
		rest = fs.Args()
		if len(rest) < 2 {
			return errors.New("give the login a name and name its fields, such as: rauli keychain add --origin https://example.com mail user password")
		}
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("%q looks like an option: put options before the name", a)
			}
		}
		add.Name, fields, add.Origins = rest[0], rest[1:], origins
		seen := map[string]bool{}
		for _, f := range fields {
			if seen[f] {
				return fmt.Errorf("the field %q is named twice", f)
			}
			seen[f] = true
		}
	}

	k, err := openManager(ctx, cfg, errOut)
	if err != nil {
		return err
	}
	if k == nil {
		return errors.New("no keychain is set up: add a [keychain] section to the configuration, with a backend of keyring, file or command")
	}
	defer k.Close()

	switch cmd {
	case "add":
		vals, err := readValues(in, errOut, add.Name, fields)
		if err != nil {
			return err
		}
		add.Fields = vals
		err = k.Add(ctx, add)
		for _, v := range vals {
			v.Wipe()
		}
		if err != nil {
			return err
		}
		where := "anywhere"
		if len(add.Origins) > 0 {
			where = strings.Join(add.Origins, ", ")
		}
		fmt.Fprintf(out, "Added %s (%s), usable on %s.\n", add.Name, strings.Join(fields, ", "), where)

	case "list":
		hs, err := k.Handles(ctx)
		if err != nil {
			return err
		}
		if len(hs) == 0 {
			fmt.Fprintln(out, "The keychain holds no logins.")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tFIELDS\tORIGINS")
		for _, h := range hs {
			fmt.Fprintf(w, "%s\t%s\t%s\n", h.Name, strings.Join(h.Fields, ", "), originsText(h.Origins))
		}
		return w.Flush()

	case "show":
		if len(rest) != 1 {
			return errors.New("name the login to show: rauli keychain show NAME")
		}
		h, err := k.Show(ctx, rest[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "name: %s\nfields: %s\norigins: %s\n", h.Name, strings.Join(h.Fields, ", "), originsText(h.Origins))

	case "remove":
		if len(rest) != 1 {
			return errors.New("name the login to remove: rauli keychain remove NAME")
		}
		if err := k.Remove(ctx, rest[0]); err != nil {
			return err
		}
		fmt.Fprintf(out, "Removed %s.\n", rest[0])

	case "test":
		if len(rest) != 2 {
			return errors.New("give the login and a page: rauli keychain test NAME https://example.com/login")
		}
		fs, err := k.Test(ctx, rest[0], rest[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "ok: %s can fill %s on %s\n", rest[0], strings.Join(fs, ", "), shownOrigin(rest[1]))
	}
	return nil
}

// manager is what the command line needs of a keychain, whichever backend
// holds the logins.
type manager interface {
	tools.Vault
	Show(ctx context.Context, name string) (tools.Handle, error)
	Test(ctx context.Context, name, pageURL string) ([]string, error)
	Add(ctx context.Context, o AddOptions) error
	Remove(ctx context.Context, name string) error
	Close() error
}

// openManager opens the keychain for the command line. A nil manager with a
// nil error means no keychain is configured.
func openManager(ctx context.Context, cfg Config, errOut io.Writer) (manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Backend == "command" {
		v, err := newCommandVault(cfg)
		if err != nil {
			return nil, err
		}
		// Only here does the program's standard error reach anyone: this is a
		// person at a terminal finding out why a login does not work.
		v.stderr = errOut
		return v, nil
	}
	k, err := open(ctx, cfg)
	if err != nil || k == nil {
		return nil, err
	}
	return k, nil
}

func originsText(o []string) string {
	if len(o) == 0 {
		return "anywhere"
	}
	return strings.Join(o, ", ")
}

// readValues reads the value of each field: from the terminal, with the secret
// ones hidden and asked for twice, or from in, one line per field.
func readValues(in io.Reader, errOut io.Writer, name string, fields []string) (map[string]Secret, error) {
	vals := make(map[string]Secret, len(fields))
	fail := func(err error) (map[string]Secret, error) {
		for _, v := range vals {
			v.Wipe()
		}
		return nil, err
	}

	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fd := int(f.Fd())
		lines := bufio.NewReader(f)
		for _, field := range fields {
			if isVisible(field) {
				fmt.Fprintf(errOut, "%s for %s: ", field, name)
				line, err := lines.ReadString('\n')
				if err != nil && line == "" {
					return fail(fmt.Errorf("no value for %s", field))
				}
				vals[field] = Secret(strings.TrimRight(line, "\r\n"))
				continue
			}
			fmt.Fprintf(errOut, "%s for %s (hidden): ", field, name)
			v, err := term.ReadPassword(fd)
			fmt.Fprintln(errOut)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(errOut, "%s again: ", field)
			again, err := term.ReadPassword(fd)
			fmt.Fprintln(errOut)
			if err != nil {
				return fail(err)
			}
			if string(again) != string(v) {
				for i := range again {
					again[i] = 0
				}
				return fail(fmt.Errorf("the two entries for %s differ", field))
			}
			for i := range again {
				again[i] = 0
			}
			vals[field] = Secret(v)
		}
		return vals, nil
	}

	r := bufio.NewReader(in)
	for _, field := range fields {
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return fail(fmt.Errorf("expected a value for %s on standard input, one line for each field in the order given", field))
		}
		vals[field] = Secret(strings.TrimRight(line, "\r\n"))
	}
	return vals, nil
}
