package client

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/prompt"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type response int

const (
	trustHost     response = iota
	dontTrustHost response = iota
)

// Represents an unknown host.
type unknownHost struct {
	server     string
	remote     net.Addr
	key        ssh.PublicKey
	hostLine   string
	ipLine     string
	responseCh chan response
}

// KnownHostsCallback is a wrapper around ssh.KnownHosts so that we can add all
// unknown hosts in a single batch to the known_hosts file.
type KnownHostsCallback struct {
	knownHostsPath  string
	knownHostsFile  fs.RootedPath
	unknownCh       chan unknownHost
	trustAllHostsCh chan struct{}
	untrustedHosts  map[string]bool
	mutex           *sync.Mutex
}

var _ HostKeyCallback = (*KnownHostsCallback)(nil)

// NewKnownHostsCallback returns a new wrapper.
func NewKnownHostsCallback(knownHostsPath string, trustAllHosts bool) (HostKeyCallback, error) {

	knownHostsFile, err := fs.NewRootedPath(knownHostsPath)
	if err != nil {
		return nil, err
	}
	ensureKnownHostsFile(knownHostsFile)
	untrustedHosts := make(map[string]bool)

	c := KnownHostsCallback{
		knownHostsPath:  knownHostsPath,
		knownHostsFile:  knownHostsFile,
		unknownCh:       make(chan unknownHost),
		trustAllHostsCh: make(chan struct{}),
		untrustedHosts:  untrustedHosts,
		mutex:           &sync.Mutex{},
	}
	if trustAllHosts {
		close(c.trustAllHostsCh)
	}
	return &c, nil
}

func ensureKnownHostsFile(knownHostsFile fs.RootedPath) {
	root, err := knownHostsFile.OpenRoot()
	if err != nil {
		return
	}
	defer root.Close()

	fd, err := root.OpenFile(knownHostsFile.Name(), os.O_RDONLY|os.O_CREATE, 0o666)
	if err != nil {
		return
	}
	fd.Close()
}

// Wrap the host key callback. The returned ssh.HostKeyCallback is bound to
// ctx: if ctx is cancelled while we are waiting for the PromptAddHosts
// goroutine to consume an unknown host or to return a user decision, the
// callback aborts with ctx.Err() instead of blocking forever. This prevents
// a stuck SSH handshake (and a leaked goroutine per unknown host) when the
// client shuts down before the user responds, or when PromptAddHosts has
// already returned because its ctx was cancelled.
func (c *KnownHostsCallback) Wrap(ctx context.Context) ssh.HostKeyCallback {
	return func(server string, remote net.Addr, key ssh.PublicKey) error {
		// Parse known_hosts file
		knownHostsCb, err := knownhosts.New(c.knownHostsPath)
		if err != nil {
			return err
		}
		// Check for valid entry in known_hosts file
		err = knownHostsCb(server, remote, key)
		if err == nil {
			// OK
			return nil
		}

		unknown := unknownHost{
			server:     server,
			remote:     remote,
			key:        key,
			hostLine:   knownhosts.Line([]string{server}, key),
			ipLine:     knownhosts.Line([]string{remote.String()}, key),
			responseCh: make(chan response, 1),
		}
		// Keep host trust discovery diagnostics out of normal command output.
		// In trust-all and plain modes this warning can corrupt tool output.
		dlog.Client.Debug("Encountered unknown host", unknown.server, unknown.remote.String())
		// Notify user that there is an unknown host. Honour ctx cancellation
		// so we do not block forever when PromptAddHosts has already exited.
		select {
		case c.unknownCh <- unknown:
		case <-ctx.Done():
			return fmt.Errorf("host key callback cancelled for %s: %w", server, ctx.Err())
		}
		// Wait for user input. Same contract as above: abort on ctx cancel.
		var resp response
		select {
		case resp = <-unknown.responseCh:
		case <-ctx.Done():
			return fmt.Errorf("host key callback cancelled for %s: %w", server, ctx.Err())
		}
		switch resp {
		case trustHost:
			// End user acknowledged host key
			return nil
		case dontTrustHost:
		}

		c.mutex.Lock()
		defer c.mutex.Unlock()
		c.untrustedHosts[server] = true
		return err
	}
}

// PromptAddHosts prompts a question to the user whether unknown hosts should
// be added to the known hosts or not.
func (c *KnownHostsCallback) PromptAddHosts(ctx context.Context) {
	var hosts []unknownHost
	for {
		// Check whether there is a unknown host
		select {
		case unknown := <-c.unknownCh:
			hosts = append(hosts, unknown)
			// Ask every 50 unknown hosts
			if len(hosts) >= 50 {
				c.promptAddHosts(hosts)
				hosts = []unknownHost{}
			}
		case <-time.After(2 * time.Second):
			// Or ask when after 2 seconds no new unknown hosts were added.
			if len(hosts) > 0 {
				c.promptAddHosts(hosts)
				hosts = []unknownHost{}
			}
		case <-ctx.Done():
			dlog.Client.Debug("Stopping goroutine prompting new hosts...")
			return
		}
	}
}

func (c *KnownHostsCallback) promptAddHosts(hosts []unknownHost) {
	var servers []string
	for _, host := range hosts {
		servers = append(servers, host.server)
	}

	select {
	case <-c.trustAllHostsCh:
		// Trust-all mode is non-interactive; avoid warning-level noise on stdout.
		dlog.Client.Debug("Trusting host keys of servers", servers)
		if err := c.trustHosts(hosts); err != nil {
			dlog.Client.Error("Unable to update known hosts file", c.knownHostsPath, err)
			c.dontTrustHosts(hosts)
		}
		return
	default:
	}

	question := fmt.Sprintf("Encountered %d unknown hosts: '%s'\n%s",
		len(servers),
		strings.Join(servers, ","),
		"Do you want to trust these hosts?",
	)
	p := prompt.New(question)

	a := prompt.Answer{
		Long:  "yes",
		Short: "y",
		Callback: func() {
			if err := c.trustHosts(hosts); err != nil {
				dlog.Client.Error("Unable to update known hosts file", c.knownHostsPath, err)
				c.dontTrustHosts(hosts)
				return
			}
			dlog.Client.Info("Added hosts to known hosts file", c.knownHostsPath)
		},
	}
	p.Add(a)

	a = prompt.Answer{
		Long:  "all",
		Short: "a",
		Callback: func() {
			if err := c.trustHosts(hosts); err != nil {
				dlog.Client.Error("Unable to update known hosts file", c.knownHostsPath, err)
				c.dontTrustHosts(hosts)
				return
			}
			select {
			case <-c.trustAllHostsCh:
			default:
				close(c.trustAllHostsCh)
			}
			dlog.Client.Info("Added hosts to known hosts file", c.knownHostsPath)
		},
	}
	p.Add(a)

	a = prompt.Answer{
		Long:  "no",
		Short: "n",
		Callback: func() {
			c.dontTrustHosts(hosts)
		},
		EndCallback: func() {
			dlog.Client.Info("Didn't add hosts to known hosts file", c.knownHostsPath)
		},
	}
	p.Add(a)

	a = prompt.Answer{
		Long:     "details",
		Short:    "d",
		AskAgain: true,
		Callback: func() {
			for _, unknown := range hosts {
				fmt.Println(unknown.hostLine)
				fmt.Println(unknown.ipLine)
			}
		},
	}
	p.Add(a)

	p.Ask()
}

func (c *KnownHostsCallback) trustHosts(hosts []unknownHost) error {
	root, err := c.knownHostsFile.OpenRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	tmpKnownHostsName := fmt.Sprintf("%s.tmp", c.knownHostsFile.Name())
	tmpKnownHostsPath := fmt.Sprintf("%s.tmp", c.knownHostsPath)
	cleanupTmp := func() {
		if err := root.Remove(tmpKnownHostsName); err != nil && !os.IsNotExist(err) {
			dlog.Client.Debug("Unable to remove temporary known hosts file", tmpKnownHostsPath, err)
		}
	}

	newFd, err := root.OpenFile(tmpKnownHostsName, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open temp known hosts file %s: %w", tmpKnownHostsPath, err)
	}
	if err := newFd.Chmod(0o600); err != nil {
		newFd.Close()
		cleanupTmp()
		return fmt.Errorf("chmod temp known hosts file %s: %w", tmpKnownHostsPath, err)
	}

	// Newly trusted hosts in normalized form
	addresses := make(map[string]struct{})
	// First write to new known hosts file, and keep track of addresses
	for _, unknown := range hosts {
		// Add once as [HOSTNAME]:PORT
		addresses[knownhosts.Normalize(unknown.server)] = struct{}{}
		// And once as [IP]:PORT
		addresses[knownhosts.Normalize(unknown.remote.String())] = struct{}{}

		if _, err := newFd.WriteString(fmt.Sprintf("%s\n", unknown.hostLine)); err != nil {
			newFd.Close()
			cleanupTmp()
			return fmt.Errorf("write host known_hosts entry: %w", err)
		}
		if _, err := newFd.WriteString(fmt.Sprintf("%s\n", unknown.ipLine)); err != nil {
			newFd.Close()
			cleanupTmp()
			return fmt.Errorf("write ip known_hosts entry: %w", err)
		}
	}

	// Read old known hosts file, to see which are old and new entries
	oldFd, err := root.OpenFile(c.knownHostsFile.Name(), os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		newFd.Close()
		cleanupTmp()
		return fmt.Errorf("open known hosts file %s: %w", c.knownHostsPath, err)
	}

	scanner := bufio.NewScanner(oldFd)
	// Now, append all still valid old entries to the new host file
	for scanner.Scan() {
		line := scanner.Text()
		address := strings.SplitN(line, " ", 2)[0]

		if _, ok := addresses[address]; !ok {
			if _, err := newFd.WriteString(fmt.Sprintf("%s\n", line)); err != nil {
				oldFd.Close()
				newFd.Close()
				cleanupTmp()
				return fmt.Errorf("append existing known_hosts entry: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		oldFd.Close()
		newFd.Close()
		cleanupTmp()
		return fmt.Errorf("scan existing known_hosts entries: %w", err)
	}

	if err := oldFd.Close(); err != nil {
		newFd.Close()
		cleanupTmp()
		return fmt.Errorf("close known hosts file %s: %w", c.knownHostsPath, err)
	}
	if err := newFd.Close(); err != nil {
		cleanupTmp()
		return fmt.Errorf("close temp known hosts file %s: %w", tmpKnownHostsPath, err)
	}

	// Now, replace old known hosts file
	if err := root.Rename(tmpKnownHostsName, c.knownHostsFile.Name()); err != nil {
		cleanupTmp()
		return fmt.Errorf("replace known_hosts file %s: %w", c.knownHostsPath, err)
	}

	for _, unknown := range hosts {
		unknown.responseCh <- trustHost
	}
	return nil
}

func (c *KnownHostsCallback) dontTrustHosts(hosts []unknownHost) {
	for _, unknown := range hosts {
		unknown.responseCh <- dontTrustHost
	}
}

// Untrusted returns true if the host is not trusted. False otherwise.
func (c *KnownHostsCallback) Untrusted(server string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	_, ok := c.untrustedHosts[server]
	return ok
}
