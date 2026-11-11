package api

// SessionFactory binds a new, independently owned host to a PTY. The returned
// close function must serialize shutdown with terminal input and painting.
type SessionFactory func(Host, TabSpawnRequest, SessionSpawn) (close func(), err error)

// SessionSpawn queues an application tab request outside the requesting PTY's
// parser lock. Replies retain the requesting terminal, not browser focus.
type SessionSpawn func(string, TabSpawnRequest, func(WidgetID, error)) bool

// SessionService owns its listener independently of any terminal window. Initial
// launches once; defaults supplies the command/directory for later API requests.
type SessionService interface {
	ServeSessions(SessionFactory, TabSpawnRequest, TabSpawnRequest) error
}
