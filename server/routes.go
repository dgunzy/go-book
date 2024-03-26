package server

import (
	"context"
	"fmt"
	"html/template"
	"net/http"

	"github.com/dgunzy/go-book/auth"
	"github.com/dgunzy/go-book/dao"
	"github.com/dgunzy/go-book/models"
	"github.com/markbates/goth/gothic"
)

func (server *Server) RegisterRoutes() http.Handler {

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir("/routing/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	userDao := dao.NewUserDAO(server.db)
	homeHandler := http.HandlerFunc(server.AuthCallbackHandler)
	homeHandlerWithMiddleware := auth.AuthMiddleware(homeHandler, userDao)
	mux.Handle("/home", homeHandlerWithMiddleware)
	// mux.HandleFunc("/home", server.AuthCallbackHandler)
	mux.HandleFunc("/logout/{provider}", server.LogoutHandler)
	mux.HandleFunc("/", server.IndexHandler)
	mux.HandleFunc("/auth/{provider}", server.AuthHandler)

	mux.HandleFunc("/dashboard", server.DashboardHandler)
	mux.HandleFunc("/test", server.TestHandler)
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

func (server *Server) IndexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/index.gohtml"))
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// func (server *Server) AuthCallbackHandler(w http.ResponseWriter, r *http.Request) {
// 	user, err := gothic.CompleteUserAuth(w, r)

// 	if err != nil {
// 		fmt.Fprintln(w, err)
// 		return
// 	}
// 	fmt.Println(user)

// 	http.Redirect(w, r, "/dashboard", http.StatusFound)

// }
func (server *Server) AuthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := gothic.Store.Get(r, "_gothic_session")
	user, ok := session.Values["user"].(*models.User)
	if !ok {
		http.Error(w, "User not found in session", http.StatusInternalServerError)
		return
	}
	fmt.Println(user)
	http.Redirect(w, r, "/dashboard", http.StatusFound)
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
func (server *Server) TestHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/test.gohtml"))
	email := "test@example.com" // Replace with the actual email value
	data := struct {
		Email string
	}{
		Email: email,
	}
	UserDAO := dao.NewUserDAO(server.db)
	newUser := &models.User{
		Email: email,
		Role:  "user",
	}
	err := UserDAO.CreateUser(newUser)

	if err != nil {
		fmt.Println(err)
	}
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (server *Server) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("static/templates/dashboard.gohtml"))
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
