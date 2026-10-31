// Package contract defines, parses, and validates the version 1 file format.
package contract

// Repository is the committed ghrepo.yaml document.
type Repository struct {
	Schema     int      `yaml:"schema"`
	Repository Repo     `yaml:"repository"`
	APT        APT      `yaml:"apt"`
	RPM        RPM      `yaml:"rpm"`
	Downloads  Download `yaml:"downloads"`
	Site       Site     `yaml:"site"`
	Storage    Storage  `yaml:"storage,omitempty"`
}

type Repo struct {
	URL    string `yaml:"url"`
	Branch string `yaml:"branch"`
	Layout string `yaml:"layout"`
}

type APT struct {
	Suite         string   `yaml:"suite"`
	Component     string   `yaml:"component"`
	Architectures []string `yaml:"architectures"`
}

type RPM struct {
	ID               string   `yaml:"id"`
	Name             string   `yaml:"name"`
	Architectures    []string `yaml:"architectures"`
	PackageSignature string   `yaml:"package_signature"`
	TrustedKeyPaths  []string `yaml:"trusted_key_paths,omitempty"`
}

type Download struct {
	SourceHosts   []string `yaml:"source_hosts"`
	RedirectHosts []string `yaml:"redirect_hosts"`
	MaxBytes      int64    `yaml:"max_bytes"`
}

type Site struct {
	BaseURL       string `yaml:"base_url"`
	MaxBytes      int64  `yaml:"max_bytes"`
	RetentionDays int    `yaml:"retention_days"`
	CNAME         string `yaml:"cname,omitempty"`
}

type Storage struct {
	Backend       string `yaml:"backend"`
	ReleasePrefix string `yaml:"release_prefix,omitempty"`
	BucketSize    int    `yaml:"bucket_size,omitempty"`
}

// Manifest is the committed packages.yaml document.
type Manifest struct {
	Schema   int       `yaml:"schema"`
	Packages []Package `yaml:"packages"`
}

type Package struct {
	Path       string   `yaml:"path"`
	URL        string   `yaml:"url"`
	SHA256     string   `yaml:"sha256"`
	Size       int64    `yaml:"size"`
	Source     string   `yaml:"source"`
	State      string   `yaml:"state"`
	Format     string   `yaml:"format"`
	Identity   Identity `yaml:"identity"`
	RetainedAt string   `yaml:"retained_at,omitempty"`
}

// Identity contains values read from the package, never from its filename.
type Identity struct {
	Name         string `yaml:"name"`
	Epoch        string `yaml:"epoch,omitempty"`
	Version      string `yaml:"version"`
	Release      string `yaml:"release,omitempty"`
	Architecture string `yaml:"architecture"`
}

// Local is the ignored .ghrepo.local.yaml document.
type Local struct {
	Schema  int          `yaml:"schema"`
	GPG     LocalGPG     `yaml:"gpg,omitempty"`
	GitHub  LocalGitHub  `yaml:"github,omitempty"`
	Storage LocalStorage `yaml:"storage,omitempty"`
}

type LocalGPG struct {
	KeyID   string `yaml:"key_id,omitempty"`
	Home    string `yaml:"home,omitempty"`
	Keyring string `yaml:"keyring,omitempty"`
}

type LocalGitHub struct {
	Account string `yaml:"account,omitempty"`
	Token   string `yaml:"token,omitempty"`
	EnvFile string `yaml:"env_file,omitempty"`
}

type LocalStorage struct {
	ReleaseRepository string `yaml:"release_repository,omitempty"`
}
