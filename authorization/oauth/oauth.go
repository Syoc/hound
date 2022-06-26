package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
)

const (
	RedirectURL string = "/api/v1/oauth/redirect"
)

var (
	GitlabConfig *oauth2.Config
	GitlabHost   string
	RedirectHost string
)

type OAuth2 struct {
	ClientID     string `json:"client-id"`
	ClientSecret string `json:"client-secret"`
	TokenURL     string `json:"token-url"`
	AuthURL      string `json:"auth-url"`
	RedirectHost string `json:"redirect-host"`
}

func (o *OAuth2) InitGitlab() error {
	log.Println("Initializing OAuth2 config")

	GitlabConfig = &oauth2.Config{
		RedirectURL:  o.RedirectHost + RedirectURL,
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		Scopes:       []string{"read_api"},
		Endpoint: oauth2.Endpoint{
			TokenURL: o.TokenURL,
			AuthURL:  o.AuthURL,
		},
	}
	// We need the Gitlab hostname for API access when we get the oauth token
	u, err := url.Parse(o.TokenURL)
	if err != nil {
		return err
	}
	GitlabHost = u.Scheme + "://" + u.Host
	// Hostname for the webserver to redirect back to after authorization
	RedirectHost = o.RedirectHost
	log.Printf("OAuth redirect URL: %s", o.RedirectHost+"://"+RedirectURL)

	return nil
}

func GetStateCookie() string {
	b := make([]byte, 16)
	rand.Read(b)
	state := base64.URLEncoding.EncodeToString(b)

	return state
}

func VerifyState(r *http.Request) error {
	oauthState, _ := r.Cookie("oauthstate")

	if r.FormValue("state") != oauthState.Value {
		return fmt.Errorf("Failed to verify state")
	}
	return nil
}

// Exchange OAuth authorization code into an access token
func GetToken(code string, config *oauth2.Config) (*oauth2.Token, error) {
	ctx := context.Background()
	token, err := config.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	return token, nil
}
