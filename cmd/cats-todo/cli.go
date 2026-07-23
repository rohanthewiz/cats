// cli.go — the `add` subcommand: quick capture into a backlog from a shell,
// keybinding, or pipe, without opening the manager UI.

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// addFromCLI implements `cats-todo add [-g] [-t title] [prompt...]`. The prompt
// is the remaining arguments joined by spaces; with none it is read from a piped
// stdin. The default target is the project backlog rooted at the nearest
// .cats-todo (or .git) directory above the current directory; -g targets the
// global backlog instead.
func addFromCLI(args []string) {
	fs := flag.NewFlagSet("cats-todo add", flag.ExitOnError)
	global := fs.Bool("g", false, "add to the global backlog instead of the project's")
	fs.BoolVar(global, "global", false, "alias for -g")
	title := fs.String("t", "", "short title (derived from the prompt's first line when blank)")
	fs.StringVar(title, "title", "", "alias for -t")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cats-todo add [-g] [-t title] [prompt...]")
		fmt.Fprintln(os.Stderr, "  the prompt is the remaining args joined; with none it is read from a piped stdin")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args) // ExitOnError: a bad flag prints usage and exits

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		prompt = strings.TrimSpace(readPipedStdin())
	}
	if prompt == "" {
		fs.Usage()
		os.Exit(2)
	}

	var st *store
	if *global {
		path, err := globalTodosPath()
		if err != nil {
			errExit("could not resolve the global backlog:", err)
		}
		st = &store{scope: scopeGlobal, path: path}
	} else {
		wd, err := os.Getwd()
		if err != nil {
			errExit("could not determine the working directory:", err)
		}
		st = &store{scope: scopeProject, path: projectTodosPath(findProjectRoot(wd))}
	}

	t := strings.TrimSpace(*title)
	if t == "" {
		t = firstLine(prompt, 60)
	}
	if err := st.add(Todo{ID: newID(), Title: t, Prompt: prompt, Created: time.Now()}); err != nil {
		errExit("could not save:", err)
	}
	fmt.Printf("added to the %s backlog (%s)\n", strings.ToLower(st.scope.String()), st.path)
}

// readPipedStdin returns everything on stdin when it is a pipe or file, and ""
// when stdin is an interactive terminal (we never block waiting for typing).
func readPipedStdin() string {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return ""
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return string(data)
}

// findProjectRoot walks up from dir to the project root for a CLI add: the
// nearest directory with an existing .cats-todo backlog wins, then the nearest
// with a .git directory (the repo root). With neither, dir itself is the root —
// matching how the manager scopes todos to the directory it launched from.
func findProjectRoot(dir string) string {
	for d := dir; ; {
		if fi, err := os.Stat(filepath.Join(d, projectConfigDirName)); err == nil && fi.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir
}
