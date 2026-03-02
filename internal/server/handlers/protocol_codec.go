package handlers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/protocol"
	user "github.com/mimecast/dtail/internal/user/server"
)

type protocolCodec struct {
	user *user.User
}

func newProtocolCodec(user *user.User) protocolCodec {
	return protocolCodec{user: user}
}

func (c protocolCodec) handleProtocolVersion(args []string) ([]string, int, string, error) {
	argc := len(args)
	var add string

	if argc <= 2 || args[0] != "protocol" {
		return args, argc, add, errors.New("unable to determine protocol version")
	}

	if args[1] != protocol.ProtocolCompat {
		clientCompat, _ := strconv.Atoi(args[1])
		serverCompat, _ := strconv.Atoi(protocol.ProtocolCompat)
		if clientCompat <= 3 {
			// Protocol version 3 or lower expect a newline as message separator
			// One day (after 2 major versions) this exception may be removed!
			add = "\n"
		}

		toUpdate := "client"
		if clientCompat > serverCompat {
			toUpdate = "server"
		}
		err := fmt.Errorf("the DTail server protocol version '%s' does not match "+
			"client protocol version '%s', please update DTail %s",
			protocol.ProtocolCompat, args[1], toUpdate)
		return args, argc, add, err
	}

	return args[2:], argc - 2, add, nil
}

func (c protocolCodec) handleBase64(args []string, argc int) ([]string, int, error) {
	err := errors.New("unable to decode client message, DTail server and client " +
		"versions may not be compatible")
	if argc != 2 || args[0] != "base64" {
		return args, argc, err
	}

	decoded, err := base64.StdEncoding.DecodeString(args[1])
	if err != nil {
		return args, argc, err
	}
	decodedStr := string(decoded)

	args = strings.Split(decodedStr, " ")
	argc = len(args)
	dlog.Server.Trace(c.user, "Base64 decoded received command",
		decodedStr, argc, args)

	return args, argc, nil
}
