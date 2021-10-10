package integrationtests

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func runCommand(t *testing.T, cmd string, args []string, stdoutFile string) (int, error) {
	return runCommandContext(t, context.TODO(), cmd, args, stdoutFile)
}

func runCommandContext(t *testing.T, ctx context.Context, cmd string, args []string, stdoutFile string) (int, error) {
	if _, err := os.Stat(cmd); err != nil {
		return -1, fmt.Errorf("No such binary %s, please compile first (%v)", cmd, err)
	}

	t.Log("Running command:", cmd, strings.Join(args, " "))
	bytes, cmdErr := exec.CommandContext(ctx, cmd, args...).Output()

	t.Log("Writing stdout to file", stdoutFile)
	fd, err := os.Create(stdoutFile)
	if err != nil {
		return -1, err
	}
	defer fd.Close()
	fd.Write(bytes)

	return exitCodeFromError(cmdErr), err
}

func exitCodeFromError(err error) int {
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			ws := exitError.Sys().(syscall.WaitStatus)
			return ws.ExitStatus()
		}
	}
	return 0
}

// Checks whether both files have the same lines (order doesn't matter)
func compareFilesContents(t *testing.T, fileA, fileB string) error {
	mapFile := func(file string) (map[string]int, error) {
		t.Log("Reading", file)
		contents := make(map[string]int)
		fd, err := os.Open(file)
		if err != nil {
			return contents, err
		}
		defer fd.Close()

		scanner := bufio.NewScanner(fd)
		scanner.Split(bufio.ScanLines)
		for scanner.Scan() {
			line := scanner.Text()
			count, _ := contents[line]
			contents[line] = count + 1
		}

		return contents, nil
	}

	compareMaps := func(a, b map[string]int) error {
		for line, countA := range a {
			countB, ok := b[line]
			if !ok {
				return fmt.Errorf("Files differ, line '%s' is missing in one of them", line)
			}
			if countA != countB {
				return fmt.Errorf("Files differ, count of line '%s' is %d in one but %d in another", line, countA, countB)
			}
		}
		return nil
	}

	a, err := mapFile(fileA)
	if err != nil {
		return err
	}
	b, err := mapFile(fileB)
	if err != nil {
		return err
	}

	// The mapreduce result can be in a different order each time (Golang maps are not sorted).
	t.Log(fmt.Sprintf("Checking whether %s has same lines as file %s (ignoring line order)", fileA, fileB))
	if err := compareMaps(a, b); err != nil {
		return err
	}
	t.Log(fmt.Sprintf("Checking whether %s has same lines as file %s (ignoring line order)", fileB, fileA))
	if err := compareMaps(b, a); err != nil {
		return err
	}

	return nil
}

func compareFiles(t *testing.T, fileA, fileB string) error {
	t.Log("Comparing files", fileA, fileB)
	shaFileA := shaOfFile(t, fileA)
	shaFileB := shaOfFile(t, fileB)

	if shaFileA != shaFileB {
		t.Errorf("Expected SHA %s but got %s", shaFileA, shaFileB)
		if bytes, err := exec.Command("diff", "-u", fileA, fileB).Output(); err != nil {
			return fmt.Errorf(string(bytes))
		}
	}

	return nil
}

func shaOfFile(t *testing.T, file string) string {
	bytes, err := ioutil.ReadFile(file)
	if err != nil {
		t.Error(err)
	}
	hasher := sha256.New()
	hasher.Write(bytes)
	sha := base64.URLEncoding.EncodeToString(hasher.Sum(nil))
	t.Log("SHA", file, sha)
	return sha
}
