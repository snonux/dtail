package server

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/fs"
	"github.com/mimecast/dtail/internal/io/fs/permissions"
)

// User represents an end-user which connected to the server via the DTail client.
type User struct {
	// The user name.
	Name string
	// The remote address connected from.
	remoteAddress string
	// The permissions the user has.
	permissions []string
}

// PermissionLookup resolves permissions for a given SSH user.
type PermissionLookup func(string) ([]string, error)

// New returns a new user.
func New(name, remoteAddress string, permissionLookup PermissionLookup) (*User, error) {
	var (
		permissions []string
		err         error
	)
	if permissionLookup != nil {
		permissions, err = permissionLookup(name)
		if err != nil {
			return nil, err
		}
	}
	return &User{
		Name:          name,
		remoteAddress: remoteAddress,
		permissions:   permissions,
	}, nil
}

// String representation of the user.
func (u *User) String() string {
	return fmt.Sprintf("%s@%s", u.Name, u.remoteAddress)
}

// HasFilePermission is used to determine whether user is allowed to read a file.
func (u *User) HasFilePermission(filePath, permissionType string) bool {
	_, hasPermission := u.ValidateReadTarget(filePath, permissionType)
	return hasPermission
}

// ValidateReadTarget resolves and authorizes a file path for server-side reads.
func (u *User) ValidateReadTarget(filePath, permissionType string) (fs.ValidatedReadTarget, bool) {
	dlog.Server.Debug(u, filePath, permissionType, "Checking config permissions")
	cleanPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		dlog.Server.Error(u, filePath, permissionType,
			"Unable to evaluate symlinks", err)
		return fs.ValidatedReadTarget{}, false
	}

	cleanPath, err = filepath.Abs(cleanPath)
	if err != nil {
		dlog.Server.Error(u, cleanPath, permissionType,
			"Unable to make file path absolute", err)
		return fs.ValidatedReadTarget{}, false
	}

	if cleanPath != filePath {
		dlog.Server.Info(u, filePath, cleanPath, permissionType,
			"Calculated new clean path from original file path (possibly symlink)")
	}

	if u.Name != config.ScheduleUser && u.Name != config.ContinuousUser {
		hasPermission, permissionErr := u.hasFilePermission(cleanPath, permissionType)
		if permissionErr != nil {
			dlog.Server.Warn(u, cleanPath, permissionErr)
		}
		if !hasPermission {
			return fs.ValidatedReadTarget{}, false
		}
	}

	target, err := fs.NewValidatedReadTarget(cleanPath)
	if err != nil {
		dlog.Server.Warn(u, cleanPath, permissionType, "Unable to validate read target", err)
		return fs.ValidatedReadTarget{}, false
	}

	return target, true
}

func (u *User) hasFilePermission(cleanPath, permissionType string) (bool, error) {
	// First check file system Linux/UNIX permission.
	if _, err := permissions.ToRead(u.Name, cleanPath); err != nil {
		return false, fmt.Errorf("User without OS file system permissions to read path: %w", err)
	}
	dlog.Server.Info(u, cleanPath, permissionType,
		"User with OS file system permissions to path")

	hasPermission, err := u.iteratePaths(cleanPath, permissionType)
	if err != nil {
		return false, err
	}

	return hasPermission, nil
}

func (u *User) iteratePaths(cleanPath, permissionType string) (bool, error) {
	// By default assume no permissions
	hasPermission := false
	for _, permission := range u.permissions {
		typeStr := "readfiles" // Assume ReadFiles by default.
		var regexStr string
		var negate bool

		splitted := strings.Split(permission, ":")
		if len(splitted) > 1 {
			typeStr = splitted[0]
			permission = strings.Join(splitted[1:], ":")
		}

		dlog.Server.Debug(u, cleanPath, typeStr, permission)
		if typeStr != permissionType {
			continue
		}

		regexStr = permission
		if strings.HasPrefix(permission, "!") {
			regexStr = permission[1:]
			negate = true
		}

		re, err := regexp.Compile(regexStr)
		if err != nil {
			return false, fmt.Errorf("Permission test failed, can't compile regex "+
				"'%s': %w", regexStr, err)
		}
		if negate && re.MatchString(cleanPath) {
			dlog.Server.Info(u, cleanPath, "Permission test failed partially, "+
				"matching negative pattern '%s'", permission)
			hasPermission = false
		}
		if !negate && re.MatchString(cleanPath) {
			dlog.Server.Info(u, cleanPath, "Permission test passed partially, "+
				"matching positive pattern", permission)
			hasPermission = true
		}
	}

	return hasPermission, nil
}
