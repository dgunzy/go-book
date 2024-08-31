package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/gob"
	"errors"
	"log"
	"net/http"
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
	if len(keyPairs) == 0 {
		keyPairs = [][]byte{securecookie.GenerateRandomKey(32)}
	}
	codecs := securecookie.CodecsFromPairs(keyPairs...)
	return &LibsqlStore{
		db:     db,
		Codecs: codecs,
		Options: &sessions.Options{
			Path:     "/",
			MaxAge:   86400 * 7,
			HttpOnly: true,
			Secure:   true, // Set to true if using HTTPS
			SameSite: http.SameSiteLaxMode,
		},
	}, nil
}

func (s *LibsqlStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	log.Printf("Attempting to get session: %s", name)
	cookie, err := r.Cookie(name)
	if err != nil {
		if err == http.ErrNoCookie {
			log.Printf("No cookie found for session: %s", name)
			return s.New(r, name)
		}
		log.Printf("Error getting cookie: %v", err)
		return nil, err
	}

	session := sessions.NewSession(s, name)
	err = securecookie.DecodeMulti(name, cookie.Value, &session.ID, s.Codecs...)
	if err != nil {
		log.Printf("Error decoding cookie: %v", err)
		// Instead of returning a new session, let's try to use the encoded value as is
		session.ID = cookie.Value
	}

	// Verify and load session from database
	var dbData string
	var expiryStr string
	err = s.db.QueryRow("SELECT data, expiry FROM sessions WHERE id = ? AND datetime(expiry) > datetime('now')", session.ID).Scan(&dbData, &expiryStr)
	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("Session not found in database or expired: %v", err)
			return s.New(r, name)
		}
		log.Printf("Error querying database: %v", err)
		return nil, err
	}

	err = securecookie.DecodeMulti(name, dbData, &session.Values, s.Codecs...)
	if err != nil {
		log.Printf("Error decoding database data: %v", err)
		return s.New(r, name)
	}

	log.Printf("Session retrieved for ID: %s", session.ID)
	return session, nil
}

func (s *LibsqlStore) New(r *http.Request, name string) (*sessions.Session, error) {
	log.Printf("Creating new session: %s", name)
	session := sessions.NewSession(s, name)
	opts := *s.Options
	session.Options = &opts
	session.IsNew = true
	return session, nil
}

func (s *LibsqlStore) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	log.Printf("Saving session: %s", session.Name())
	if session.ID == "" {
		session.ID = generateSessionID()
	}

	encoded, err := securecookie.EncodeMulti(session.Name(), session.Values, s.Codecs...)
	if err != nil {
		log.Printf("Error encoding session values: %v", err)
		return err
	}

	expiryTime := time.Now().Add(time.Duration(session.Options.MaxAge) * time.Second).UTC()
	_, err = s.db.Exec("INSERT OR REPLACE INTO sessions (id, data, expiry) VALUES (?, ?, datetime(?))",
		session.ID, encoded, expiryTime.Format("2006-01-02 15:04:05"))
	if err != nil {
		log.Printf("Error saving session to database: %v", err)
		return err
	}

	encodedID, err := securecookie.EncodeMulti(session.Name(), session.ID, s.Codecs...)
	if err != nil {
		log.Printf("Error encoding session ID: %v", err)
		return err
	}

	cookie := sessions.NewCookie(session.Name(), encodedID, session.Options)
	cookie.Expires = time.Now().Add(time.Duration(session.Options.MaxAge) * time.Second)
	http.SetCookie(w, cookie)
	log.Printf("Session saved and cookie set for ID: %s, Name: %s, Value: %s, MaxAge: %d, Expires: %s, HttpOnly: %v, Secure: %v, SameSite: %v, Domain: %s",
		session.ID, cookie.Name, cookie.Value, cookie.MaxAge, cookie.Expires, cookie.HttpOnly, cookie.Secure, cookie.SameSite, cookie.Domain)
	return nil
}

func generateSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Printf("Error generating session ID: %v", err)
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)
}
