package loggers

import (
	"fmt"
	"strings"
	"sync"
)

var factoryMap map[string]Logger
var factoryMutex sync.Mutex

type loggerConstructor func(Strategy) Logger

var loggerRegistry = map[string]loggerConstructor{
	"none": func(Strategy) Logger { return none{} },
	"stdout": func(Strategy) Logger {
		return newStdout()
	},
	"file": func(strategy Strategy) Logger {
		return newFile(strategy)
	},
	"fout": func(strategy Strategy) Logger {
		return newFout(strategy)
	},
}

// Factory is there to retrieve a logger based on various settings.
func Factory(sourceName, loggerName string, logRotation Strategy) (Logger, error) {
	factoryMutex.Lock()
	defer factoryMutex.Unlock()

	loggerName = strings.ToLower(loggerName)
	constructor, ok := loggerRegistry[loggerName]
	if !ok {
		return nil, fmt.Errorf("unsupported logger type %q", loggerName)
	}
	id := fmt.Sprintf("sourceName:%s,fileBase:%s,loggerName:%s", sourceName,
		logRotation.FileBase, loggerName)
	if factoryMap == nil {
		factoryMap = make(map[string]Logger)
	}

	singleton, ok := factoryMap[id]
	if !ok {
		singleton = constructor(logRotation)
		factoryMap[id] = singleton
	}
	return singleton, nil
}

// FactoryRotate invokes a log rotation of all loggers.
func FactoryRotate() {
	factoryMutex.Lock()
	defer factoryMutex.Unlock()
	if factoryMap == nil {
		return
	}
	for _, logger := range factoryMap {
		if rotator, ok := logger.(Rotator); ok {
			rotator.Rotate()
		}
	}
}
