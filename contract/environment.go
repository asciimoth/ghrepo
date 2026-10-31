package contract

import (
	"bufio"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnv reads key-value data. It never expands variables or runs commands.
func ParseEnv(data []byte) (map[string]string, error) {
	result := map[string]string{}
	s := bufio.NewScanner(strings.NewReader(string(data)))
	s.Buffer(make([]byte, 4096), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKeyRE.MatchString(key) || key == "export" {
			return nil, errors.New("invalid .env assignment")
		}
		if _, exists := result[key]; exists {
			return nil, errors.New("duplicate .env key")
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return nil, errors.New("invalid .env quoted value")
			}
			if value[0] == '"' {
				var err error
				value, err = strconv.Unquote(value)
				if err != nil {
					return nil, errors.New("invalid .env quoted value")
				}
			} else {
				value = value[1 : len(value)-1]
			}
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid .env value")
		}
		result[key] = value
	}
	if s.Err() != nil {
		return nil, errors.New("invalid .env input")
	}
	return result, nil
}

// EffectiveLocal applies process environment, selected .env, and local settings
// in descending precedence. Shared configuration remains the non-secret base.
func EffectiveLocal(local Local, selectedEnv map[string]string, processEnv map[string]string) Local {
	choose := func(key, fallback string) string {
		if v, ok := processEnv[key]; ok {
			return v
		}
		if v, ok := selectedEnv[key]; ok {
			return v
		}
		return fallback
	}
	local.GitHub.Token = choose("GH_TOKEN", local.GitHub.Token)
	local.GitHub.Account = choose("GHREPO_GITHUB_ACCOUNT", local.GitHub.Account)
	local.GPG.KeyID = choose("GHREPO_GPG_KEY_ID", local.GPG.KeyID)
	local.GPG.Home = choose("GHREPO_GPG_HOME", local.GPG.Home)
	local.GPG.Keyring = choose("GHREPO_GPG_KEYRING", local.GPG.Keyring)
	local.Storage.ReleaseRepository = choose("GHREPO_RELEASE_REPOSITORY", local.Storage.ReleaseRepository)
	return local
}

func LoadEffectiveLocal(name string, processEnv map[string]string) (Local, error) {
	l, err := ReadLocal(name)
	if err != nil {
		return Local{}, err
	}
	selected := map[string]string{}
	if l.GitHub.EnvFile != "" {
		data, err := os.ReadFile(l.GitHub.EnvFile)
		if err != nil {
			return Local{}, err
		}
		selected, err = ParseEnv(data)
		if err != nil {
			return Local{}, err
		}
	}
	l = EffectiveLocal(l, selected, processEnv)
	if err := l.Validate(); err != nil {
		return Local{}, err
	}
	return l, nil
}
