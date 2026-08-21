package config

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
)

// boolFlag is a stdlib flag.Value for a boolean that implements IsBoolFlag
// so `--collector.qp-counters` does not consume the next argv.
type boolFlag struct {
	v *bool
}

func (b *boolFlag) String() string {
	if b == nil || b.v == nil {
		return "false"
	}
	return strconv.FormatBool(*b.v)
}

func (b *boolFlag) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*b.v = v
	return nil
}

func (b *boolFlag) IsBoolFlag() bool { return true }

// noCollectorFlag sets the pointed bool to false. It is IsBoolFlag so a bare
// `--no-collector.X` works. Valued forms (`--no-collector.X=true`) are rejected
// before Parse by rejectValuedNoCollectorFlags.
type noCollectorFlag struct {
	v *bool
}

func (n *noCollectorFlag) String() string {
	if n == nil || n.v == nil {
		return "false"
	}
	// DefValue of the hidden negation flag is not shown in help.
	return strconv.FormatBool(!*n.v)
}

func (n *noCollectorFlag) Set(s string) error {
	if s != "true" {
		return fmt.Errorf("does not take a value")
	}
	*n.v = false
	return nil
}

func (n *noCollectorFlag) IsBoolFlag() bool { return true }

func registerCollectorFlag(fs *flag.FlagSet, name string, dst *bool, usage string) {
	fs.Var(&boolFlag{v: dst}, "collector."+name, usage)
	fs.Var(&noCollectorFlag{v: dst}, "no-collector."+name, "")
}

func parseFlagName(arg string) (name string, hasValue bool, isFlag bool) {
	if arg == "--" || !strings.HasPrefix(arg, "-") {
		return "", false, false
	}
	s := arg
	if strings.HasPrefix(s, "--") {
		s = s[2:]
	} else {
		s = s[1:]
	}
	if s == "" {
		return "", false, false
	}
	name, _, hasValue = strings.Cut(s, "=")
	return name, hasValue, true
}

func rejectRemovedAndValuedNoCollector(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		name, hasValue, isFlag := parseFlagName(arg)
		if !isFlag {
			continue
		}
		if replacement, ok := removedFlagReplacements[name]; ok {
			return fmt.Errorf("flag --%s has been removed; %s", name, replacement)
		}
		if strings.HasPrefix(name, "no-collector.") && hasValue {
			return fmt.Errorf("flag --%s does not take a value", name)
		}
	}
	return nil
}

func printFlagDefaults(fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		if strings.HasPrefix(f.Name, "no-collector.") {
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "  -%s", f.Name)
		name, usage := flag.UnquoteUsage(f)
		if len(name) > 0 {
			b.WriteString(" ")
			b.WriteString(name)
		}
		if b.Len() <= 4 {
			b.WriteString("\t")
		} else {
			b.WriteString("\n    \t")
		}
		b.WriteString(strings.ReplaceAll(usage, "\n", "\n    \t"))
		if f.DefValue != "" && f.DefValue != "false" {
			fmt.Fprintf(&b, " (default %s)", f.DefValue)
		}
		fmt.Fprint(fs.Output(), b.String(), "\n")
	})
}
