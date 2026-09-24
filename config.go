package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type config struct {
	// Hosts are the GitHub hosts whose pull requests the Mine and Review
	// tabs list: github.com, and any GitHub Enterprise host gh is signed in to.
	Hosts []string `json:"hosts"`
	// Repos maps "owner/repo" (or "host/owner/repo") to a local checkout, for
	// repos the plugin can't find among herdr's spaces by their remotes.
	Repos map[string]string `json:"repos"`
	// CloneRoot is where a repo with no checkout is cloned, as
	// <clone_root>/<owner>/<repo>, after asking.
	CloneRoot string `json:"clone_root"`
	// StartPrompt is sent to the new worktree's agent by "start". {number},
	// {url}, {repo}, {branch} and {title} are substituted.
	StartPrompt string `json:"start_prompt"`
	// AgentWaitSeconds bounds how long "start" waits for an agent to come up in
	// the new worktree before giving up on sending the prompt.
	AgentWaitSeconds int `json:"agent_wait_seconds"`
	// Labels turns the sidebar tokens ($pr, $pr_badge, …) on or off.
	Labels *bool `json:"labels"`
	// LabelRefreshSeconds is how old a repo's PR status may get before a
	// herdr event refreshes it from GitHub.
	LabelRefreshSeconds int `json:"label_refresh_seconds"`
	// Theme for rendered Markdown: "dark", "light", or empty to ask the terminal.
	Theme string `json:"theme"`
}

func (c config) labelsOn() bool { return c.Labels == nil || *c.Labels }

func pluginID() string {
	if id := os.Getenv("HERDR_PLUGIN_ID"); id != "" {
		return id
	}
	return "herdr-github"
}

func xdgDir(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

func configDir() string {
	if d := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "herdr", "plugins", "config", pluginID())
}

func stateDir() string {
	if d := os.Getenv("HERDR_PLUGIN_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"), "herdr", "plugins", pluginID())
}

// loadConfig reads config.json. On error it still returns the defaults, so
// the label hook can keep working (quietly) while the file is broken.
func loadConfig() (config, error) {
	cfg, err := readConfig()
	if err != nil {
		cfg = config{}
	}
	return withDefaults(cfg), err
}

func readConfig() (config, error) {
	cfg := config{}
	data, err := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, errors.New("config.json: " + err.Error())
		}
	}
	return cfg, nil
}

func withDefaults(cfg config) config {
	if len(cfg.Hosts) == 0 {
		cfg.Hosts = []string{"github.com"}
	}
	for i, h := range cfg.Hosts {
		cfg.Hosts[i] = strings.ToLower(strings.TrimSpace(h))
	}
	if cfg.CloneRoot == "" {
		cfg.CloneRoot = "~/code"
	}
	cfg.CloneRoot = expandHome(cfg.CloneRoot)
	if cfg.StartPrompt == "" {
		cfg.StartPrompt = "Pull request {url} is checked out here. Read it with `gh pr view {number} --comments`, look at any failing checks, and tell me what's left to do."
	}
	if cfg.AgentWaitSeconds <= 0 {
		cfg.AgentWaitSeconds = 90
	}
	if cfg.LabelRefreshSeconds <= 0 {
		cfg.LabelRefreshSeconds = 60
	}
	repos := map[string]string{}
	for k, v := range cfg.Repos {
		repos[strings.ToLower(k)] = expandHome(v)
	}
	cfg.Repos = repos
	return cfg
}

// knownHost says whether h is one of the configured GitHub hosts.
func (c config) knownHost(h string) bool {
	for _, x := range c.Hosts {
		if strings.EqualFold(x, h) {
			return true
		}
	}
	return false
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
