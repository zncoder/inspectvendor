package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/zncoder/cli"
)

// copied from vendorspec
type PkgSpec struct {
	// Import path. Example "rsc.io/pdf".
	// go get <Path> should fetch the remote package.
	Path string `json:"path"`

	// Origin is an import path where it was copied from. This import path
	// may contain "vendor" segments.
	//
	// If empty or missing origin is assumed to be the same as the Path field.
	Origin string `json:"origin"`

	// The revision of the package. This field must be persisted by all
	// tools, but not all tools will interpret this field.
	// The value of Revision should be a single value that can be used
	// to fetch the same or similar revision.
	// Examples: "abc104...438ade0", "v1.3.5"
	Revision string `json:"revision"`

	// RevisionTime is the time the revision was created. The time should be
	// parsed and written in the "time.RFC3339" format.
	RevisionTime string `json:"revisionTime"`

	// Comment is free text for human use.
	Comment string `json:"comment,omitempty"`
}

type Spec struct {
	Comment string `json:"comment,omitempty"`

	// Package represents a collection of vendor packages that have been copied
	// locally. Each entry represents a single Go package.
	Pkgs []*PkgSpec `json:"package"`
}

func (ps *PkgSpec) String() string {
	if ps.Origin == "" {
		return fmt.Sprintf("%s@%s", ps.Path, ps.Revision)
	}
	return fmt.Sprintf("%s<<%s@%s", ps.Path, ps.Origin, ps.Revision)
}

var repo = flag.String("r", "", "repo dir. the vendor.json is in the vendor subdir")
var verbose = flag.Bool("v", false, "verbose logging")

func main() {
	defer func() {
		if w != nil {
			fmt.Fprintf(w, "Package\tVersion\tImported By\n")
			w.Flush()
		}
	}()

	cli.Define("list", doList)
	cli.Define("dir", doDirect)
	cli.Define("indir", doIndirect)
	cli.Define("showversion", doShowVersion)
	cli.Main()
}

func doList() {
	cli.ParseFlag(initCommon)

	spec := readSpec(*repo, "")

	for _, p := range spec.Pkgs {
		fmt.Fprintf(w, "%s\t%s\n", p.Path, p.Revision)
	}
}

func doDirect() {
	cli.ParseFlag(initCommon)

	spec := readSpec(*repo, "")

	for _, p := range spec.Pkgs {
		if p.Origin != "" {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\n", p.Path, p.Revision)
	}
}

func doIndirect() {
	cli.ParseFlag(initCommon)

	spec := readSpec(*repo, "")
	for _, p := range spec.Pkgs {
		if p.Origin == "" {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\n", p.Path, p.Origin)
	}
}

type VersionOrigin struct {
	Version string
	Origin  string
}

func (vo VersionOrigin) String() string {
	if vo.Origin == "" {
		return vo.Version
	}
	return fmt.Sprintf("%s@%s", vo.Origin, vo.Version)
}

func doShowVersion() {
	maxDepth := flag.Int("depth", 5, "maximum number of indirections")
	pkgToCheck := flag.String("p", "", "check this package only")
	cli.ParseFlag(initCommon)

	spec := readSpec(*repo, "")
	for _, p := range spec.Pkgs {
		if *pkgToCheck != "" && *pkgToCheck != p.Path {
			continue
		}

		if p.Origin == "" {
			fmt.Fprintf(w, "%s\t%s\n", p.Path, p.Revision)
			continue
		}

		vos := findTrueVersion(p, *maxDepth)
		lgf("true version of pkg=p.Path: %v", vos)
		ver := "UNKNOWN"
		if len(vos) > 1 {
			ver = vos[len(vos)-1].Version
		}
		fmt.Fprintf(w, "%s\t%s\t%s@%s\n", p.Path, ver, p.Origin, p.Revision)
	}
}

func findTrueVersion(p *PkgSpec, depth int) []VersionOrigin {
	lgf("to find true verson of pkg=%s origin=%s at ver=%s depth=%d",
		p.Path, p.Origin, p.Revision, depth)
	out := []VersionOrigin{{Version: p.Revision, Origin: p.Revision}}

	rp := vendorRepo(p)
	lgf("vendorrepo=%s", rp)
	if rp == "" {
		return out
	}

	spec := readSpec(rp, p.Revision)
	if spec == nil {
		lgf("no spec in repo=%s at ver=%s", rp, p.Revision)
		return out
	}

	var up *PkgSpec
	for _, x := range spec.Pkgs {
		if x.Path == p.Path {
			lgf("found pkg=%v", x)
			up = x
			break
		}
	}
	if up == nil {
		lgf("pkg=%s is not found in spec=%s@%s", p.Path, rp, p.Revision)
		return nil
	}

	if up.Origin != "" {
		lgf("up.origin=%s recursing", up.Origin)
		depth--
		if depth <= 0 {
			log.Fatalf("max depth reached in finding true version of pkg=%s in origin=%s",
				p.Path, p.Origin)
		}

		vos := findTrueVersion(up, depth)
		out = append(out, vos...)
	} else {
		out = append(out, VersionOrigin{Version: up.Revision, Origin: up.Origin})
	}
	return out
}

func vendorRepo(ps *PkgSpec) string {
	i := strings.Index(ps.Origin, "/vendor/")
	if i < 0 {
		log.Fatalf("no vendor in pkgspec=%v", ps)
	}
	p := ps.Origin[:i]

	for _, gp := range gopaths {
		r := filepath.Join(gp, "src", p)
		lgf("check vendor in path=%s", r)
		if _, err := os.Stat(filepath.Join(r, "vendor/vendor.json")); err == nil {
			return r
		}
	}
	return ""
}

var w *tabwriter.Writer
var gopaths []string

func initCommon() bool {
	w = tabwriter.NewWriter(os.Stdout, 8, 8, 4, ' ', 0)

	gopaths = filepath.SplitList(os.Getenv("GOPATH"))

	d, err := filepath.Abs(*repo)
	if err != nil {
		log.Fatalf("abs of repo=%s err=%v", *repo, err)
	}
	*repo = d
	return true
}

func readSpec(dir, ver string) *Spec {
	lgf("read spec=%s at ver=%s", dir, ver)
	if ver != "" {
		gd := gitDir(dir)
		if gd == "" {
			lgf("dir=%s is not in git", dir)
			return nil
		}
		defer os.Chdir(dir)
		os.Chdir(gd)
	}

	dir = relPath(dir)

	fn := filepath.Join(dir, "vendor/vendor.json")

	var b []byte
	var err error
	if ver == "" {
		b, err = ioutil.ReadFile(fn)
	} else {
		var buf bytes.Buffer
		var errBuf bytes.Buffer
		cmd := exec.Command("git", "show", ver+":"+fn)
		cmd.Stdout = &buf
		cmd.Stderr = &errBuf
		if err = cmd.Run(); err == nil {
			b = buf.Bytes()
		} else {
			err = fmt.Errorf("git show err=%v stderr=%s", err, errBuf.Bytes())
		}
	}
	if err != nil {
		if ver == "" {
			log.Fatalf("read vendor=%s err=%v", fn, err)
		} else {
			lgf("read vendor=%s at ver=%s err=%v", fn, ver, err)
		}
		return nil
	}

	spec := &Spec{}
	if err = json.Unmarshal(b, spec); err != nil {
		log.Fatalf("unmarshal spec of vendor=%s err=%v", fn, err)
	}
	return spec
}

func gitDir(dir string) string {
	for dir != "" {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
	}
	return ""
}

func lgf(format string, v ...interface{}) {
	if !*verbose {
		return
	}
	log.Printf(format, v...)
}

func relPath(dir string) string {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("getwd err=%s", err)
	}
	rel, err := filepath.Rel(wd, dir)
	if err != nil {
		log.Fatalf("rel wd=%s dir=%s err=%v", wd, dir, err)
	}
	return rel
}
