package session

import (
	"fmt"
	"strings"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/regex"
)

// Spec captures the mutable, per-connection workload a DTail client wants to run.
type Spec struct {
	Mode        omode.Mode `json:"mode"`
	Files       []string   `json:"files"`
	Options     string     `json:"options,omitempty"`
	Query       string     `json:"query,omitempty"`
	Regex       string     `json:"regex,omitempty"`
	RegexInvert bool       `json:"regex_invert,omitempty"`
	Timeout     int        `json:"timeout,omitempty"`
}

// NewSpec returns a session specification from client args.
func NewSpec(args config.Args) Spec {
	return Spec{
		Mode:        args.Mode,
		Files:       splitFiles(args.What),
		Options:     args.SerializeOptions(),
		Query:       strings.TrimSpace(args.QueryStr),
		Regex:       args.RegexStr,
		RegexInvert: args.RegexInvert,
		Timeout:     args.Timeout,
	}
}

// Commands returns the legacy command stream for this session specification.
func (s Spec) Commands() ([]string, error) {
	switch {
	case s.Mode == omode.HealthClient:
		return []string{"health"}, nil
	case s.Query != "":
		return s.queryCommands()
	default:
		return s.readCommands(s.Mode.String())
	}
}

func (s Spec) queryCommands() ([]string, error) {
	if s.Mode != omode.MapClient && s.Mode != omode.TailClient {
		return nil, fmt.Errorf("session spec query mode requires map or tail mode, got %s", s.Mode)
	}

	regexValue, err := s.serializedRegex()
	if err != nil {
		return nil, err
	}

	commands := []string{fmt.Sprintf("map:%s %s", s.Options, s.Query)}
	readMode := "cat"
	if s.Mode == omode.TailClient {
		readMode = "tail"
	}

	for _, file := range s.Files {
		if s.Timeout > 0 {
			commands = append(commands, fmt.Sprintf("timeout %d %s %s %s", s.Timeout, readMode, file, regexValue))
			continue
		}
		commands = append(commands, fmt.Sprintf("%s:%s %s %s", readMode, s.Options, file, regexValue))
	}

	return commands, nil
}

func (s Spec) readCommands(mode string) ([]string, error) {
	switch s.Mode {
	case omode.TailClient, omode.CatClient, omode.GrepClient:
	default:
		return nil, fmt.Errorf("unsupported session mode %s", s.Mode)
	}

	regexValue, err := s.serializedRegex()
	if err != nil {
		return nil, err
	}

	var commands []string
	for _, file := range s.Files {
		commands = append(commands, fmt.Sprintf("%s:%s %s %s", mode, s.Options, file, regexValue))
	}

	return commands, nil
}

func (s Spec) serializedRegex() (string, error) {
	flag := regex.Default
	if s.RegexInvert {
		flag = regex.Invert
	}

	re, err := regex.New(s.Regex, flag)
	if err != nil {
		return "", err
	}

	return re.Serialize()
}

func splitFiles(what string) []string {
	if strings.TrimSpace(what) == "" {
		return nil
	}

	rawFiles := strings.Split(what, ",")
	files := make([]string, 0, len(rawFiles))
	for _, file := range rawFiles {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		files = append(files, file)
	}
	return files
}
