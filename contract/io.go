package contract

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

func decodeStrict(data []byte, out any) error {
	var node yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&node); err != nil {
		return errors.New("invalid YAML syntax")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("YAML must contain one document")
	}
	if err := checkNode(&node); err != nil {
		return err
	}
	dec = yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		// A field value could be a credential. Do not include decoder text.
		return errors.New("invalid YAML fields or values")
	}
	return nil
}

func checkNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!merge" {
		return errors.New("YAML aliases, anchors, and merges are not supported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] || key.Value == "<<" {
				return errors.New("duplicate or invalid YAML key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := checkNode(child); err != nil {
			return err
		}
	}
	return nil
}

func ParseRepository(data []byte) (Repository, error) {
	var r Repository
	if err := decodeStrict(data, &r); err != nil {
		return Repository{}, err
	}
	return r, r.Validate()
}

func ParseManifest(data []byte, r Repository) (Manifest, error) {
	var m Manifest
	if err := decodeStrict(data, &m); err != nil {
		return Manifest{}, err
	}
	return m, m.Validate(r)
}

func ParseLocal(data []byte) (Local, error) {
	var l Local
	if err := decodeStrict(data, &l); err != nil {
		return Local{}, err
	}
	return l, l.Validate()
}

func ReadRepository(name string) (Repository, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Repository{}, err
	}
	return ParseRepository(b)
}

func ReadManifest(name string, r Repository) (Manifest, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Manifest{}, err
	}
	return ParseManifest(b, r)
}

func ReadLocal(name string) (Local, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Local{}, err
	}
	l, err := ParseLocal(b)
	if err != nil {
		return Local{}, err
	}
	if l.GitHub.Token != "" {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			return Local{}, errors.New("local config with token must have mode 0600")
		}
	}
	return l, nil
}

func MarshalRepository(r Repository) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(r)
}

func MarshalManifest(m Manifest, r Repository) ([]byte, error) {
	if err := m.Validate(r); err != nil {
		return nil, err
	}
	copyM := Manifest{Schema: m.Schema, Packages: append([]Package(nil), m.Packages...)}
	sort.Slice(copyM.Packages, func(i, j int) bool { return copyM.Packages[i].Path < copyM.Packages[j].Path })
	return yaml.Marshal(copyM)
}

func MarshalLocal(l Local) ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(l)
}

func WriteRepository(name string, r Repository) error {
	data, err := MarshalRepository(r)
	if err != nil {
		return err
	}
	return WriteAtomic(name, data, 0644)
}

func WriteManifest(name string, m Manifest, r Repository) error {
	data, err := MarshalManifest(m, r)
	if err != nil {
		return err
	}
	return WriteAtomic(name, data, 0644)
}

func WriteLocal(name string, l Local) error {
	data, err := MarshalLocal(l)
	if err != nil {
		return err
	}
	return WriteAtomic(name, data, 0600)
}

// WriteAtomic replaces a file only after its complete contents reach a temporary file.
func WriteAtomic(name string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(name)
	f, err := os.CreateTemp(dir, ".ghrepo-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), name); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err == nil {
		defer func() { _ = d.Close() }()
		return d.Sync()
	}
	return err
}
