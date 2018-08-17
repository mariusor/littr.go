package app

import (
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/juju/errors"
	"github.com/mariusor/littr.go/models"
	"golang.org/x/crypto/bcrypt"
)

const SessionUserKey = "__current_acct"

type loginModel struct {
	Title         string
	InvertedTheme bool
	Account       models.Account
}

// ShowLogin handles POST /login requests
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	errs := make([]error, 0)
	pw := r.PostFormValue("pw")
	handle := r.PostFormValue("handle")
	a, err := models.Service.LoadAccount(models.LoadAccountFilter{Handle: handle})
	if err != nil {
		log.Print(err)
		HandleError(w, r, StatusUnknown, errors.Errorf("handle or password are wrong"))
		return
	}
	m := a.Metadata
	log.Printf("Loaded pw: %q, salt: %q", m.Password, m.Salt)
	salt := m.Salt
	saltedpw := []byte(pw)
	saltedpw = append(saltedpw, salt...)
	err = bcrypt.CompareHashAndPassword([]byte(m.Password), saltedpw)
	if err != nil {
		log.Print(err)
		HandleError(w, r, StatusUnknown, errors.Errorf("handle or password are wrong"))
		return
	}

	s := GetSession(r)
	s.Values[SessionUserKey] = a
	CurrentAccount = &a
	AddFlashMessage(Success, "Login successful", r, w)

	err = SessionStore.Save(r, w, s)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		HandleError(w, r, http.StatusInternalServerError, errs...)
		return
	}
	Redirect(w, r, "/", http.StatusSeeOther)
	return
}

// ShowLogin serves GET /login requests
func ShowLogin(w http.ResponseWriter, r *http.Request) {
	a := models.Account{}

	m := loginModel{Title: "Login", InvertedTheme: isInverted(r)}
	m.Account = a

	RenderTemplate(r, w, "login", m)
}

// HandleLogout serves /logout requests
func HandleLogout(w http.ResponseWriter, r *http.Request) {
	s := GetSession(r)
	s.Values[SessionUserKey] = nil
	SessionStore.Save(r, w, s)
	CurrentAccount = AnonymousAccount()
	Redirect(w, r, "/", http.StatusSeeOther)
}
