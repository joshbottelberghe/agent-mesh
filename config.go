package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the ONLY place a node's local layout lives. The shared repo ships
// config.example.yaml with placeholder paths; each operator copies it to
// config.yaml (gitignored) and points it at their own private data. No personal
// path or datum ever enters the repo.
type Config struct {
	NodeName   string   `yaml:"node_name"`
	ListenAddr string   `yaml:"listen_addr"` // local bind used when ngrok is disabled
	Ngrok      struct {
		Enabled bool   `yaml:"enabled"`
		Domain  string `yaml:"domain"` // optional reserved domain; empty = random URL
	} `yaml:"ngrok"`
	Data struct {
		TasksDir  string   `yaml:"tasks_dir"`
		NotesDirs []string `yaml:"notes_dirs"`
	} `yaml:"data"`
	// Peers is phase 2: other nodes this one may call. Tokens come from env
	// (TokenEnv), never inline, so config.yaml carries no secrets either.
	Peers []Peer `yaml:"peers"`
}

type Peer struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	TokenEnv string `yaml:"token_env"`
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.NodeName == "" {
		c.NodeName = "unnamed-node"
	}
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:7900"
	}
	return &c, nil
}
