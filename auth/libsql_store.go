package auth

import (
	"database/sql"
	"encoding/gob"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
)

type LibsqlStore struct {
	db      *sql.DB
	Codecs  []securecookie.Codec
	Options *sessions.Options
}

func init() {
	gob.Register(map[string]interface{}{})
}

func NewLibsqlStore(db *sql.DB, keyPairs ...[]byte) (*LibsqlStore, error) {
	if db == nil {
		return nil, errors.New("db is nil")
	}

	codecs := securecookie.CodecsFromPairs(keyPairs...)
	return &LibsqlStore{
		db:     db,
		Codecs: codecs,
		Options: &sessions.Options{
			Path:   "/",
			MaxAge: 86400 * 7,
		},
	}, nil
}

func (s *LibsqlStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(s, name)
}

func (s *LibsqlStore) New(r *http.Request, name string) (*sessions.Session, error) {
	session := sessions.NewSession(s, name)
	opts := *s.Options
	session.Options = &opts
	session.IsNew = true
	return session, nil
}

func (s *LibsqlStore) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	if session.ID == "" {
		session.ID = generateSessionID()
	}
	encoded, err := securecookie.EncodeMulti(session.Name(), session.Values, s.Codecs...)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT OR REPLACE INTO sessions (id, data, expiry) VALUES (?, ?, ?)",
		session.ID, encoded, time.Now().Add(time.Duration(session.Options.MaxAge)*time.Second))
	return err
}

func (s *LibsqlStore) load(session *sessions.Session) error {
	var data string
	err := s.db.QueryRow("SELECT data FROM sessions WHERE id = ?", session.ID).Scan(&data)
	if err != nil {
		return err
	}
	return securecookie.DecodeMulti(session.Name(), data, &session.Values, s.Codecs...)
}

func generateSessionID() string {

	return os.Getenv("SESSION")
}
