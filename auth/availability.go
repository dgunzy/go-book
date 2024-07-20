package auth

import "sync"

var (
	applicationOnline bool = true
	mutex             sync.Mutex
)

// SetApplicationOnline sets the application to online
func SetApplicationOnline() {
	mutex.Lock()
	defer mutex.Unlock()
	applicationOnline = true
}

// SetApplicationOffline sets the application to offline
func SetApplicationOffline() {
	mutex.Lock()
	defer mutex.Unlock()
	applicationOnline = false
}

// IsApplicationOnline returns true if the application is online
func IsApplicationOnline() bool {
	mutex.Lock()
	defer mutex.Unlock()
	return applicationOnline
}
