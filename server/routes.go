package server

import (
	"context"
	"fmt"
	"html/template"
	"net/http"

	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/dao"
	"github.com/markbates/goth/gothic"
)

func (server *Server) RegisterRoutes() http.Handler {

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir("/routing/static"))
	userDao := dao.NewUserDAO(server.db)
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/", server.HomeHandler)
	authCallbackWithMiddleware := http.HandlerFunc(server.AuthCallbackHandler)
	mux.HandleFunc("/home", func(w http.ResponseWriter, r *http.Request) {
		auth.AuthMiddleware(authCallbackWithMiddleware, userDao).ServeHTTP(w, r)
	})
	mux.HandleFunc("/sign-up", server.SignUpHandler)
	mux.HandleFunc("/logout/{provider}", server.LogoutHandler)
	mux.HandleFunc("/auth/{provider}", server.AuthHandler)

	return mux
}

var userTemplate = `
<p><a href="/logout/{{.Provider}}">logout</a></p>
<p>Name: {{.Name}} [{{.LastName}}, {{.FirstName}}]</p>
<p>Email: {{.Email}}</p>
<p>NickName: {{.NickName}}</p>
<p>Location: {{.Location}}</p>
<p>AvatarURL: {{.AvatarURL}} <img src="{{.AvatarURL}}"></p>
<p>Description: {{.Description}}</p>
<p>UserID: {{.UserID}}</p>
<p>AccessToken: {{.AccessToken}}</p>
<p>ExpiresAt: {{.ExpiresAt}}</p>
<p>RefreshToken: {{.RefreshToken}}</p>
`

func (server *Server) HomeHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/index.gohtml"))
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (server *Server) AuthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	user, err := gothic.CompleteUserAuth(w, r)

	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	fmt.Println(user)
	t, _ := template.New("foo").Parse(userTemplate)
	t.Execute(w, user)
}

func (server *Server) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	gothic.Logout(w, r)
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusTemporaryRedirect)
}

func (server *Server) AuthHandler(w http.ResponseWriter, r *http.Request) {
	// Extract the provider from the request URL
	// provider := r.URL.Query().Get("provider")
	// fmt.Println("Provider:", provider)
	provider := r.PathValue("provider")
	r = r.WithContext(context.WithValue(r.Context(), "provider", provider))
	if gothUser, err := gothic.CompleteUserAuth(w, r); err == nil {
		t, _ := template.New("foo").Parse(userTemplate)
		t.Execute(w, gothUser)
	} else {
		gothic.BeginAuthHandler(w, r)
	}
}

func (server *Server) SignUpHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/signup.gohtml"))
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
