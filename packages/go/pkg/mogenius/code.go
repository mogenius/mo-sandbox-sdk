package mogenius

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// pythonBootstrap is what CodeRun starts a Python snippet with. It runs the
// snippet as __main__ with the same argv, __file__, exit code and traceback
// as python3 file.py, and when the snippet draws with matplotlib it prints
// every figure as one chartMarker line — on plt.show() and for what is still
// open at the end. Without matplotlib it only runs the snippet. The
// TypeScript SDK carries the same program; a test keeps the two identical.
//
//go:embed code_run.py
var pythonBootstrap string

// chartMarker starts the line the bootstrap prints for each figure; the rest of the line is the chart JSON.
const chartMarker = "__mo_chart__:"

// interpreters CodeRun launches, by language.
var interpreters = map[types.CodeLanguage]struct{ extension, command string }{
	// the bootstrap runs the snippet and turns matplotlib figures into chart lines; it travels base64-encoded too
	types.CodeLanguagePython: {"py",
		`python3 -c "$(printf %s '` + base64.StdEncoding.EncodeToString([]byte(pythonBootstrap)) + `' | base64 -d)"`},
	types.CodeLanguageTypeScript: {"ts", "tsx"},
	types.CodeLanguageJavaScript: {"js", "node"},
}

// shellQuote single-quotes a value for a POSIX shell.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// buildCodeRunCommand is the command line that writes the snippet to a temp
// file, runs it with the language's interpreter and removes the file again,
// preserving the interpreter's exit code. Everything the user wrote is inside
// the base64 literal; argv entries are quoted one by one.
func buildCodeRunCommand(code string, language types.CodeLanguage, argv []string) (string, error) {
	interpreter, ok := interpreters[language]
	if !ok {
		return "", sdkerrors.NewMogeniusValidationError(fmt.Sprintf("Unknown language %q: use python, typescript or javascript.", language), nil)
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	command := fmt.Sprintf(`__mo_f=/tmp/mo_code_$$.%s; printf %%s '%s' | base64 -d > "$__mo_f" && %s "$__mo_f"`,
		interpreter.extension, base64.StdEncoding.EncodeToString([]byte(code)), interpreter.command)
	if len(quoted) > 0 {
		command += " " + strings.Join(quoted, " ")
	}
	return command + `; __mo_rc=$?; rm -f "$__mo_f"; exit $__mo_rc`, nil
}

// extractCharts splits the chart lines out of stdout; the rest is the program's own output.
func extractCharts(stdout string) (string, []types.Chart) {
	var charts []types.Chart
	var text strings.Builder
	// line by line, endings kept: a chart's JSON is the whole rest of its line
	for _, line := range strings.SplitAfter(stdout, "\n") {
		if payload, ok := strings.CutPrefix(line, chartMarker); ok {
			var chart types.Chart
			if json.Unmarshal([]byte(payload), &chart) == nil {
				charts = append(charts, chart)
				continue
			}
			// not a chart after all: the line stays
		}
		text.WriteString(line)
	}
	return text.String(), charts
}
