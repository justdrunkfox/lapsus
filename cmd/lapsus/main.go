// Command lapsus fixes text typed with the wrong RU/EN keyboard layout
// on niri (Wayland). Roadmap and status: see TODO.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voev/lapsus/analyze"
	"github.com/voev/lapsus/config"
	"github.com/voev/lapsus/dict"
	"github.com/voev/lapsus/fix"
	"github.com/voev/lapsus/layout"
	"github.com/voev/lapsus/niri"
	"github.com/voev/lapsus/wayland"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "check":
		if err := runCheck(); err != nil {
			fmt.Fprintln(os.Stderr, "check failed:", err)
			os.Exit(1)
		}
	case "convert":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: lapsus convert <text>")
			os.Exit(2)
		}
		fmt.Println(convertText(strings.Join(os.Args[2:], " ")))
	case "fix":
		runFixCommand(os.Args[2:])
	case "version":
		fmt.Println("lapsus", version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `lapsus — RU/EN layout fixer for niri (Wayland)

Usage:
  lapsus check              self-test: config, dictionaries, analyzer
  lapsus convert <text>     convert text to the other layout (auto direction)
  lapsus fix [flags]        one-shot: fix the last typed word (bind to an niri hotkey)
      -n, --dry-run         report what would happen, change nothing
      -v, --verbose         log each step to stderr
  lapsus version            print version

Planned (see TODO.md):
  lapsus daemon             background auto-fix mode
`)
}

// runFixCommand implements `lapsus fix`: capture the last typed word in
// the focused window and replace it if the dictionaries are confident it
// was typed in the wrong layout.
func runFixCommand(args []string) {
	fs := flag.NewFlagSet("fix", flag.ContinueOnError)
	var dryRun, verbose bool
	fs.BoolVar(&dryRun, "dry-run", false, "report what would happen, change nothing")
	fs.BoolVar(&dryRun, "n", false, "shorthand for --dry-run")
	fs.BoolVar(&verbose, "v", false, "log each step to stderr")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lapsus fix: unexpected argument %q\n", fs.Arg(0))
		os.Exit(2)
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lapsus fix:", err)
		os.Exit(1)
	}
	d := dict.New()
	if err := d.LoadUserDict(cfg.Dictionary.UserDir); err != nil {
		fmt.Fprintln(os.Stderr, "lapsus fix:", err)
		os.Exit(1)
	}
	fixer := &fix.Fixer{
		Cfg:  cfg,
		Dict: d,
		Ana:  analyze.New(d),
		Niri: &niri.Client{},
		Way:  &wayland.Tools{Pause: time.Duration(cfg.Fix.PauseMs) * time.Millisecond},
	}
	err = fixer.Run(fix.Options{DryRun: dryRun, Verbose: verbose})
	if err == nil || errors.Is(err, fix.ErrBusy) {
		// ErrBusy means the hotkey was pressed while a previous fix is
		// still running; staying quiet is the right response.
		return
	}
	fmt.Fprintln(os.Stderr, "lapsus fix:", err)
	os.Exit(1)
}

func runCheck() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	d := dict.New()
	if err := d.LoadUserDict(cfg.Dictionary.UserDir); err != nil {
		return fmt.Errorf("user dictionary: %w", err)
	}
	fmt.Printf("dictionaries: en=%d ru=%d (user dir: %s)\n",
		d.Count(layout.LayoutEN), d.Count(layout.LayoutRU), cfg.Dictionary.UserDir)

	a := analyze.New(d)
	if got, fix := a.Analyze("ghbdtn", layout.LayoutEN); !fix || got != "привет" {
		return fmt.Errorf("analyze(ghbdtn) = %q, fix=%v; want %q, fix=true", got, fix, "привет")
	}
	if _, fix := a.Analyze("руддщ", layout.LayoutRU); !fix {
		return fmt.Errorf("analyze(руддщ, RU) should need a fix")
	}
	if _, fix := a.Analyze("привет", layout.LayoutRU); fix {
		return fmt.Errorf("analyze(привет, RU) should not need a fix")
	}
	if _, fix := a.Analyze("xkqzjwep", layout.LayoutEN); fix {
		return fmt.Errorf("analyze(unknown word) should not need a fix")
	}
	if back := layout.Map(layout.Map("Hello, world!", layout.LayoutEN, layout.LayoutRU), layout.LayoutRU, layout.LayoutEN); back != "Hello, world!" {
		return fmt.Errorf("layout round trip broken: %q", back)
	}
	fmt.Println("check OK")
	return nil
}

// convertText detects the current layout (more Cyrillic → RU, else EN)
// and converts the text to the other one by physical key position.
func convertText(text string) string {
	current := analyze.GuessLayout(text)
	return layout.Map(text, current, layout.Other(current))
}

func loadConfig() (*config.Config, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return config.Defaults(), nil
	}
	cfg, err := config.LoadFile(filepath.Join(dir, "lapsus", "config.toml"))
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
