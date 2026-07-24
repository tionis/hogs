package main

import "sync"

var serverOperationLocks sync.Map

func serverOperationLock(server *ServerConfig) *sync.Mutex {
	lock, _ := serverOperationLocks.LoadOrStore(server.Unit, &sync.Mutex{})
	return lock.(*sync.Mutex)
}
