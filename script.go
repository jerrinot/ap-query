package main

import (
	_ "embed"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func newScriptCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "script [flags] <file>",
		Short:              "Starlark scripting for custom analysis",
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmdScript(args)
		},
	}
}

//go:embed script_help.txt
var scriptHelpText string

// scriptExitCode is returned by cmdScript to indicate the script's exit status.
type scriptExitCode struct {
	code int
}

func (e *scriptExitCode) Error() string {
	return fmt.Sprintf("exit code %d", e.code)
}

func cmdScript(args []string) {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(scriptHelpText)
			return
		}
		if a == "--" {
			break
		}
	}

	var inline string
	var scriptFile string
	var scriptArgs []string
	timeout := 30 * time.Second

	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			scriptArgs = args[i+1:]
			break
		}
		switch a {
		case "-c":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: -c requires a script string")
				os.Exit(2)
			}
			inline = args[i+1]
			i += 2
		case "--timeout":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --timeout requires a duration value")
				os.Exit(2)
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid --timeout value %q: %v\n", args[i+1], err)
				os.Exit(2)
			}
			timeout = d
			i += 2
		default:
			if scriptFile == "" && inline == "" {
				scriptFile = a
				i++
			} else {
				fmt.Fprintf(os.Stderr, "error: unexpected argument %q\n", a)
				os.Exit(2)
			}
		}
	}

	if inline == "" && scriptFile == "" {
		fmt.Fprintln(os.Stderr, "error: script command requires -c '<code>' or a script file")
		os.Exit(2)
	}

	code := runScript(inline, scriptFile, scriptArgs, timeout)
	if code != 0 {
		os.Exit(code)
	}
}

type extraPredeclared struct {
	name  string
	value starlark.Value
}

func withPredeclared(name string, value starlark.Value) extraPredeclared {
	return extraPredeclared{name: name, value: value}
}

func runScript(inline, scriptFile string, scriptArgs []string, timeout time.Duration, extras ...extraPredeclared) int {
	fileOpts := &syntax.FileOptions{
		Set:             true,
		While:           true,
		TopLevelControl: true,
		GlobalReassign:  true,
		Recursion:       true,
	}

	// Build ARGS list.
	argsElems := make([]starlark.Value, len(scriptArgs))
	for i, a := range scriptArgs {
		argsElems[i] = starlark.String(a)
	}
	argsList := starlark.NewList(argsElems)

	// Pre-declared globals.
	predeclared := starlark.StringDict{
		"ARGS":     argsList,
		"fail":     starlark.NewBuiltin("fail", builtinFail),
		"warn":     starlark.NewBuiltin("warn", builtinWarn),
		"open":     starlark.NewBuiltin("open", builtinOpen),
		"emit":     starlark.NewBuiltin("emit", builtinEmit),
		"emit_all": starlark.NewBuiltin("emit_all", builtinEmitAll),
		"match":    starlark.NewBuiltin("match", builtinMatch),
		"diff":     starlark.NewBuiltin("diff", builtinDiff),
		"round":    starlark.NewBuiltin("round", builtinRound),
		"pad":      starlark.NewBuiltin("pad", builtinPad),
	}
	for _, e := range extras {
		predeclared[e.name] = e.value
	}

	thread := &starlark.Thread{
		Name: "script",
		Print: func(_ *starlark.Thread, msg string) {
			fmt.Println(msg)
		},
	}

	// Set up timeout.
	timer := time.AfterFunc(timeout, func() {
		thread.Cancel("script timed out")
	})
	defer timer.Stop()

	var err error
	if inline != "" {
		_, err = starlark.ExecFileOptions(fileOpts, thread, "<inline>", inline, predeclared)
	} else {
		_, err = starlark.ExecFileOptions(fileOpts, thread, scriptFile, nil, predeclared)
	}

	if err != nil {
		var exitErr *scriptExitCode
		if errors.As(err, &exitErr) {
			return exitErr.code
		}
		if evalErr, ok := err.(*starlark.EvalError); ok {
			fmt.Fprintf(os.Stderr, "error: %s\n", evalErr.Msg)
			return 2
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}
	return 0
}

func builtinFail(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var msg string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &msg); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, msg)
	return nil, &scriptExitCode{code: 1}
}

func builtinWarn(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var msg string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &msg); err != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, msg)
	return starlark.None, nil
}

func builtinOpen(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	var event string
	var start string
	var end string
	var thread string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"path", &path,
		"event?", &event,
		"start?", &start,
		"end?", &end,
		"thread?", &thread,
	); err != nil {
		return nil, err
	}

	if event == "" {
		event = "cpu"
	}
	switch event {
	case "cpu", "wall", "alloc", "lock":
	default:
		return nil, fmt.Errorf("open: unknown event type %q (valid: cpu, wall, alloc, lock)", event)
	}

	if isJFRPath(path) {
		opts := parseOpts{fromNanos: -1, toNanos: -1}

		if start != "" {
			d, err := time.ParseDuration(start)
			if err != nil {
				return nil, fmt.Errorf("open: invalid start %q: %v", start, err)
			}
			opts.fromNanos = d.Nanoseconds()
			opts.collectTimestamps = true
		}
		if end != "" {
			d, err := time.ParseDuration(end)
			if err != nil {
				return nil, fmt.Errorf("open: invalid end %q: %v", end, err)
			}
			opts.toNanos = d.Nanoseconds()
			opts.collectTimestamps = true
		}

		parsed, err := parseJFRData(path, allJFREventTypes(), opts)
		if err != nil {
			return nil, fmt.Errorf("open: %v", err)
		}

		if parsed.eventCounts[event] == 0 {
			available := make([]string, 0, len(parsed.eventCounts))
			for e := range parsed.eventCounts {
				available = append(available, e)
			}
			if len(available) == 0 {
				return nil, fmt.Errorf("open: no events found in %s", path)
			}
			return nil, fmt.Errorf("open: event %q not found in %s (available: %s)", event, path, strings.Join(available, ", "))
		}

		sf := parsed.stacksByEvent[event]
		if sf == nil {
			sf = &stackFile{}
		}
		if thread != "" {
			sf = sf.filterByThread(thread)
		}
		return newStarlarkProfile(sf, parsed, event, path), nil
	}

	// Collapsed text.
	sf, _, err := openInput(path, event)
	if err != nil {
		return nil, fmt.Errorf("open: %v", err)
	}
	if thread != "" {
		sf = sf.filterByThread(thread)
	}
	return newStarlarkProfile(sf, nil, event, path), nil
}

func builtinEmit(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var stackVal *starlarkStack
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &stackVal); err != nil {
		return nil, err
	}
	st := stackVal.st
	tp := threadPrefix(st.thread)
	fmt.Printf("%s%s %d\n", tp, strings.Join(st.frames, ";"), st.count)
	return starlark.None, nil
}

func builtinEmitAll(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var profileVal *starlarkProfile
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &profileVal); err != nil {
		return nil, err
	}
	for i := range profileVal.sf.stacks {
		st := &profileVal.sf.stacks[i]
		tp := threadPrefix(st.thread)
		fmt.Printf("%s%s %d\n", tp, strings.Join(st.frames, ";"), st.count)
	}
	return starlark.None, nil
}

func builtinMatch(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s, pattern string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &s, &pattern); err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("match: invalid regex %q: %v", pattern, err)
	}
	return starlark.Bool(re.MatchString(s)), nil
}

func builtinDiff(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var a, bProf *starlarkProfile
	minDelta := 0.5
	var top int
	var fqn bool
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"a", &a,
		"b", &bProf,
		"min_delta?", &minDelta,
		"top?", &top,
		"fqn?", &fqn,
	); err != nil {
		return nil, err
	}
	return computeScriptDiff(a.sf, bProf.sf, minDelta, top, fqn), nil
}

func builtinRound(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var xVal starlark.Value
	var decimals int
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "x", &xVal, "decimals?", &decimals); err != nil {
		return nil, err
	}
	x, ok := starlark.AsFloat(xVal)
	if !ok {
		return nil, fmt.Errorf("round: x must be a number, got %s", xVal.Type())
	}
	if decimals < 0 {
		decimals = 0
	}
	if decimals > 15 {
		decimals = 15
	}
	mult := math.Pow(10, float64(decimals))
	return starlark.Float(math.Round(x*mult) / mult), nil
}

func valToString(v starlark.Value) string {
	if s, ok := v.(starlark.String); ok {
		return string(s)
	}
	if b, ok := v.(starlark.Bytes); ok {
		return string(b)
	}
	return v.String()
}

func builtinPad(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var val starlark.Value
	var width int
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "value", &val, "width", &width); err != nil {
		return nil, err
	}
	s := valToString(val)
	abs := width
	if abs < 0 {
		abs = -abs
	}
	if abs < 0 { // overflow: -math.MinInt == math.MinInt
		return nil, fmt.Errorf("pad: width out of range")
	}
	charLen := utf8.RuneCountInString(s)
	if charLen >= abs {
		return starlark.String(s), nil
	}
	pad := strings.Repeat(" ", abs-charLen)
	if width < 0 {
		return starlark.String(s + pad), nil // left-align
	}
	return starlark.String(pad + s), nil // right-align
}
