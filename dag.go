package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/build"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/zncoder/cli"
)

type ImportGraph struct {
	Imports     map[string][]string
	ImportPaths map[string]string
	Std         map[string]struct{}

	srcDir     string
	includeStd bool
	todo       []string
	added      []string
}

type matcher struct {
	re *regexp.Regexp
}

func newMatcher(p string) matcher {
	var m matcher
	if p != "" {
		m.re = regexp.MustCompile(p)
	}
	return m
}

func (mr matcher) Match(pn string) bool {
	if mr.re == nil {
		return true
	}
	i := strings.Index(pn, "/vendor/")
	if i >= 0 {
		pn = pn[i+len("/vendor/"):]
	}
	return mr.re.MatchString(pn)
}

func doDAG() {
	includeStd := flag.Bool("std", false, "include packages in stdlib")
	outputFormat := flag.String("f", "flat", "output format: flat, text, dot, or svg. 'dot' and 'svg' requires the dot program")
	svgViewer := flag.String("svgviewer", "xdg-open", "svg viewer")
	matches := flag.String("m", "", "show only packages that match this regexp")
	cli.ParseFlag()

	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("getwd err=%v", err)
	}

	ig := NewImportGraph(wd, *includeStd, flag.Args())
	ig.Scan()

	mr := newMatcher(*matches)

	switch *outputFormat {
	case "dot":
		ig.WriteDot(os.Stdout, mr)
	case "svg":
		ig.ShowGraph(*svgViewer, mr)
	case "text":
		ig.WriteText(os.Stdout, mr)
	default:
		ig.WriteFlat(os.Stdout, mr)
	}
}

func NewImportGraph(wd string, includeStd bool, args []string) *ImportGraph {
	ig := &ImportGraph{
		Imports:     map[string][]string{},
		ImportPaths: map[string]string{},
		Std:         map[string]struct{}{},
		srcDir:      wd,
		includeStd:  includeStd,
	}
	ig.list(args)
	return ig
}

func (ig *ImportGraph) Scan() {
	for len(ig.todo) > 0 {
		more := ig.addPkg(ig.todo[0])
		ig.todo = append(more, ig.todo[1:]...)
	}
}

func (ig *ImportGraph) addPkg(pn string) []string {
	//log.Printf("add pkg=%s", pn)
	if ig.skip(pn) {
		return nil
	}

	p, err := build.Import(pn, ig.srcDir, 0)
	if err != nil {
		log.Printf("import pkg=%s err=%v", pn, err)
		return nil
	}

	if p.Goroot {
		ig.Std[pn] = struct{}{}
		if !ig.includeStd {
			return nil
		}
	}

	if p.ImportPath == "" {
		log.Printf("pkg=%s has empty ImportPath", pn)
		return nil
	}

	sort.Strings(p.Imports)
	ig.Imports[pn] = p.Imports
	ig.ImportPaths[pn] = p.ImportPath
	ig.added = append(ig.added, pn)

	var more []string
	for _, pi := range p.Imports {
		if !ig.skip(pi) {
			more = append(more, pi)
		}
	}
	return more
}

func (ig *ImportGraph) skip(pn string) bool {
	// ignore `import "C"` and "golang_org/x" that is vendored in go/src
	if pn == "C" || strings.HasPrefix(pn, "golang_org/x/") {
		return true
	}
	if _, ok := ig.Imports[pn]; ok {
		return true
	}
	if ig.includeStd {
		if _, ok := ig.Std[pn]; ok {
			return true
		}
	}
	return false
}

func (ig *ImportGraph) list(args []string) {
	var buf bytes.Buffer
	args = append([]string{"list"}, args...)
	c := exec.Command("go", args...)
	c.Stderr = os.Stderr
	c.Stdout = &buf
	err := c.Run()
	if err != nil {
		log.Fatalf("exec %v err=%v", c.Args, err)
	}

	var pkgs []string
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		p := sc.Text()
		pkgs = append(pkgs, p)
	}
	ig.todo = pkgs
}

func (ig *ImportGraph) WriteText(w io.Writer, mr matcher) {
	for _, pn := range ig.added {
		if !mr.Match(pn) {
			continue
		}

		var filtered []string
		for _, pi := range ig.Imports[pn] {
			if !ig.includeStd {
				if _, ok := ig.Std[pi]; ok {
					continue
				}
			}
			if !mr.Match(pi) {
				continue
			}
			filtered = append(filtered, pi)
		}
		if len(filtered) == 0 {
			continue
		}

		fmt.Fprintf(w, "%s <= %s\n", pn, ig.ImportPaths[pn])
		for _, pi := range filtered {
			fmt.Fprintf(w, "    %s\n", pi)
		}
	}
}

func (ig *ImportGraph) WriteFlat(w io.Writer, mr matcher) {
	var out []string
	for _, pn := range ig.added {
		if !mr.Match(pn) {
			continue
		}
		out = append(out, pn)
	}
	sort.Strings(out)

	for _, p := range out {
		fmt.Println(p)
	}
}

func (ig *ImportGraph) WriteDot(w io.Writer, mr matcher) {
	nodes := make(map[string]int)
	for i, pn := range ig.added {
		nodes[pn] = i
	}

	fmt.Fprintf(w, "digraph pkgdag {\n")
	for i, pn := range ig.added {
		if !mr.Match(pn) {
			continue
		}

		var filtered []string
		for _, pi := range ig.Imports[pn] {
			if !ig.includeStd {
				if _, ok := ig.Std[pi]; ok {
					continue
				}
			}
			if !mr.Match(pi) {
				continue
			}
			filtered = append(filtered, pi)
		}
		if len(filtered) == 0 {
			continue
		}

		if pv := ig.ImportPaths[pn]; strings.Contains(pv, "/vendor/") {
			fmt.Fprintf(w, "    %d [label=\"%s\",style=filled];\n", i, pn)
		} else {
			fmt.Fprintf(w, "    %d [label=\"%s\"];\n", i, pn)
		}
		for _, pi := range filtered {
			fmt.Fprintf(w, "    %d -> %d;\n", i, nodes[pi])
		}
	}
	fmt.Fprintf(w, "}\n")
}

func run(name string, stdin io.Reader, args ...string) {
	c := exec.Command(name, args...)
	c.Stdin = stdin
	c.Stderr = os.Stderr
	c.Stdout = os.Stdout
	if err := c.Run(); err != nil {
		log.Fatalf("run cmd=%v err=%v", c.Args, err)
	}
}

func (ig *ImportGraph) ShowGraph(svgViewer string, mr matcher) {
	var buf bytes.Buffer
	ig.WriteDot(&buf, mr)

	f, err := ioutil.TempFile("", "pkgdag-")
	if err != nil {
		log.Fatalf("create tempfile err=%v", err)
	}
	fn := f.Name()
	f.Close()
	os.Rename(fn, fn+".svg")
	fn = fn + ".svg"

	run("dot", &buf, "-Tsvg", "-o"+fn)
	run(svgViewer, nil, fn)
	os.Remove(fn)
}
