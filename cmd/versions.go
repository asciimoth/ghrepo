package cmd

import (
	"cmp"
	"strings"
	"time"

	"github.com/asciimoth/ghrepo/contract"
)

// retainOlderVersions keeps versions that compare equal. Package managers can
// treat different version strings as equal; neither is then an older version.
func retainOlderVersions(m *contract.Manifest, now time.Time) []string {
	type group struct{ format, name, arch string }
	latest := map[group]contract.Package{}
	for _, p := range m.Packages {
		if p.State != "active" {
			continue
		}
		key := group{p.Format, p.Identity.Name, p.Identity.Architecture}
		best, found := latest[key]
		if !found || comparePackageVersions(p, best) > 0 {
			latest[key] = p
		}
	}
	var paths []string
	for i := range m.Packages {
		p := &m.Packages[i]
		if p.State != "active" {
			continue
		}
		best := latest[group{p.Format, p.Identity.Name, p.Identity.Architecture}]
		if comparePackageVersions(*p, best) < 0 {
			p.State = "retained"
			p.RetainedAt = now.UTC().Format(time.RFC3339)
			paths = append(paths, p.Path)
		}
	}
	return paths
}

func comparePackageVersions(a, b contract.Package) int {
	if a.Format == "deb" {
		ae, av, ar := debVersionParts(a.Identity.Version)
		be, bv, br := debVersionParts(b.Identity.Version)
		if c := compareNumber(ae, be); c != 0 {
			return c
		}
		if c := compareDebPart(av, bv); c != 0 {
			return c
		}
		return compareDebPart(ar, br)
	}
	if c := compareNumber(a.Identity.Epoch, b.Identity.Epoch); c != 0 {
		return c
	}
	if c := compareRPMPart(a.Identity.Version, b.Identity.Version); c != 0 {
		return c
	}
	return compareRPMPart(a.Identity.Release, b.Identity.Release)
}

func debVersionParts(v string) (epoch, version, revision string) {
	epoch, version, revision = "0", v, "0"
	if i := strings.IndexByte(version, ':'); i >= 0 {
		epoch, version = version[:i], version[i+1:]
	}
	if i := strings.LastIndexByte(version, '-'); i >= 0 {
		version, revision = version[:i], version[i+1:]
	}
	return
}

func digit(c byte) bool  { return c >= '0' && c <= '9' }
func letter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func compareNumber(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}
func takeRun(s string, match func(byte) bool) (string, string) {
	i := 0
	for i < len(s) && match(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// Debian Policy, section 5.6.12: compare alternating non-digit and digit runs.
func compareDebPart(a, b string) int {
	order := func(s string) int {
		if s == "" || digit(s[0]) {
			return 0
		}
		if s[0] == '~' {
			return -1
		}
		if letter(s[0]) {
			return int(s[0])
		}
		return int(s[0]) + 256
	}
	for a != "" || b != "" {
		for a != "" && !digit(a[0]) || b != "" && !digit(b[0]) {
			if c := cmp.Compare(order(a), order(b)); c != 0 {
				return c
			}
			if a != "" {
				a = a[1:]
			}
			if b != "" {
				b = b[1:]
			}
		}
		var an, bn string
		an, a = takeRun(a, digit)
		bn, b = takeRun(b, digit)
		if c := compareNumber(an, bn); c != 0 {
			return c
		}
	}
	return 0
}

// RPM compares alphanumeric segments, with ~ before a release and ^ after
// that release but before its next segment. Separators have no ordering weight.
func compareRPMPart(a, b string) int {
	separator := func(c byte) bool { return !digit(c) && !letter(c) && c != '~' && c != '^' }
	for a != "" || b != "" {
		_, a = takeRun(a, separator)
		_, b = takeRun(b, separator)
		at, bt := strings.HasPrefix(a, "~"), strings.HasPrefix(b, "~")
		if at || bt {
			if !at {
				return 1
			}
			if !bt {
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		ac, bc := strings.HasPrefix(a, "^"), strings.HasPrefix(b, "^")
		if ac || bc {
			if a == "" {
				return -1
			}
			if b == "" {
				return 1
			}
			if !ac {
				return 1
			}
			if !bc {
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		if a == "" || b == "" {
			return cmp.Compare(len(a), len(b))
		}
		numeric := digit(a[0])
		if numeric != digit(b[0]) {
			if numeric {
				return 1
			}
			return -1
		}
		match := letter
		if numeric {
			match = digit
		}
		var as, bs string
		as, a = takeRun(a, match)
		bs, b = takeRun(b, match)
		c := strings.Compare(as, bs)
		if numeric {
			c = compareNumber(as, bs)
		}
		if c != 0 {
			return c
		}
	}
	return 0
}
