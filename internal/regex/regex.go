package regex

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mimecast/dtail/internal/io/logger"
)

type Regex struct {
	// The original regex string
	regexStr string
	// The Golang regexp object
	re *regexp.Regexp
	// For now only use the first flag at flags[0], but in the future we can
	// set and use multiple flags.
	flags       []Flag
	initialized bool
}

func (r Regex) String() string {
	return fmt.Sprintf("Regex(regexStr:%s,flags:%s,initialized:%t,re==nil:%t)",
		r.regexStr, r.flags, r.initialized, r.re == nil)
}

func NewNoop() Regex {
	return Regex{
		flags:       []Flag{Noop},
		initialized: true,
	}
}

func New(regexStr string, flag Flag) (Regex, error) {
	if regexStr == "" || regexStr == "." || regexStr == ".*" {
		return NewNoop(), nil
	}
	return new(regexStr, []Flag{flag})
}

func new(regexStr string, flags []Flag) (Regex, error) {
	r := Regex{
		regexStr: regexStr,
		flags:    flags,
	}

	re, err := regexp.Compile(regexStr)

	if err != nil {
		return r, err
	}

	r.re = re
	r.initialized = true
	return r, nil
}

func (r Regex) Match(bytes []byte) bool {
	switch r.flags[0] {
	case Default:
		return r.re.Match(bytes)
	case Invert:
		return !r.re.Match(bytes)
	case Noop:
		return true
	default:
		return false
	}
}

func (r Regex) MatchString(str string) bool {
	switch r.flags[0] {
	case Default:
		return r.re.MatchString(str)
	case Invert:
		return !r.re.MatchString(str)
	case Noop:
		return true
	default:
		return false
	}
}

func (r Regex) Serialize() string {
	var flags []string
	for _, flag := range r.flags {
		flags = append(flags, flag.String())
	}

	if !r.initialized {
		logger.FatalExit("Unable to serialize regex as not initialized properly", r)
	}

	return fmt.Sprintf("regex:%s %s", strings.Join(flags, ","), r.regexStr)
}

func Deserialize(str string) (Regex, error) {
	// Get regex string
	s := strings.SplitN(str, " ", 2)
	if len(s) < 2 {
		logger.Debug("Using noop regex", str)
		return NewNoop(), nil
	}

	flagsStr := s[0]
	regexStr := s[1]

	if !strings.HasPrefix(flagsStr, "regex") {
		return Regex{}, fmt.Errorf("unable to deserialize regex '%s': should start with string 'regex'", str)
	}

	// Parse regex flags, e.g. "regex:flag1,flag2,flag3..."
	var flags []Flag
	if strings.Contains(flagsStr, ":") {
		s := strings.SplitN(flagsStr, ":", 2)
		for _, flagStr := range strings.Split(s[1], ",") {
			flag, err := NewFlag(flagStr)
			if err != nil {
				logger.Error("ignoring flag", err)
				continue
			}
			logger.Debug("Adding regex flag", flag)
			flags = append(flags, flag)
		}
	}

	return new(regexStr, flags)
}
