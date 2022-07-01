package api

import (
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hound-search/hound/authorization/oauth"
	"github.com/hound-search/hound/config"
	"github.com/hound-search/hound/index"
	"github.com/hound-search/hound/searcher"

	"github.com/alexedwards/scs/v2"
)

const (
	defaultLinesOfContext uint = 2
	maxLinesOfContext     uint = 20
)

type Stats struct {
	FilesOpened int
	Duration    int
}

var session *scs.SessionManager

func InitSession() *scs.SessionManager {
	session = scs.New()
	session.Lifetime = time.Hour * 24 * 5

	return session
}

func writeJson(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Panicf("Failed to encode JSON: %v\n", err)
	}
}

func writeResp(w http.ResponseWriter, data interface{}) {
	writeJson(w, data, http.StatusOK)
}

func writeError(w http.ResponseWriter, err error, status int) {
	writeJson(w, map[string]string{
		"Error": err.Error(),
	}, status)
}

type searchResponse struct {
	repo string
	res  *index.SearchResponse
	err  error
}

/**
 * Searches all repos in parallel.
 */
func searchAll(
	query string,
	opts *index.SearchOptions,
	repos []string,
	idx map[string]*searcher.Searcher,
	filesOpened *int,
	duration *int) (map[string]*index.SearchResponse, error) {

	startedAt := time.Now()

	n := len(repos)

	// use a buffered channel to avoid routine leaks on errs.
	ch := make(chan *searchResponse, n)
	for _, repo := range repos {
		go func(repo string) {
			fms, err := idx[repo].Search(query, opts)
			ch <- &searchResponse{repo, fms, err}
		}(repo)
	}

	res := map[string]*index.SearchResponse{}
	for i := 0; i < n; i++ {
		r := <-ch
		if r.err != nil {
			return nil, r.err
		}

		if r.res.Matches == nil {
			continue
		}

		res[r.repo] = r.res
		*filesOpened += r.res.FilesOpened
	}

	*duration = int(time.Now().Sub(startedAt).Seconds() * 1000) //nolint

	return res, nil
}

// Used for parsing flags from form values.
func parseAsBool(v string) bool {
	v = strings.ToLower(v)
	return v == "true" || v == "1" || v == "fosho"
}

func parseAsRepoList(
	v string,
	idx map[string]*searcher.Searcher,
	accessKey string,
	authRepos config.StringSet) []string {

	v = strings.TrimSpace(v)
	var repos []string
	if v == "*" {
		for repo := range idx {
			if idx[repo].Repo.CheckAccess(accessKey, authRepos) {
				repos = append(repos, repo)
			}
		}
		return repos
	}

	for _, repo := range strings.Split(v, ",") {
		if idx[repo] == nil {
			continue
		}
		if idx[repo].Repo.CheckAccess(accessKey, authRepos) {
			repos = append(repos, repo)
		}
	}
	return repos
}

func parseAsUintValue(sv string, min, max, def uint) uint {
	iv, err := strconv.ParseUint(sv, 10, 54)
	if err != nil {
		return def
	}
	if max != 0 && uint(iv) > max {
		return max
	}
	if min != 0 && uint(iv) < min {
		return max
	}
	return uint(iv)
}

func parseRangeInt(v string, i *int) {
	*i = 0
	if v == "" {
		return
	}

	vi, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return
	}

	*i = int(vi)
}

func parseRangeValue(rv string) (int, int) {
	ix := strings.Index(rv, ":")
	if ix < 0 {
		return 0, 0
	}

	var b, e int
	parseRangeInt(rv[:ix], &b)
	parseRangeInt(rv[ix+1:], &e)
	return b, e
}

func Setup(m *http.ServeMux, idx map[string]*searcher.Searcher) {
	// Handle user requests for authorization
	m.HandleFunc("/api/v1/oauth/gitlab", func(w http.ResponseWriter, r *http.Request) {
		if oauth.GitlabConfig == nil {
			http.Error(w, "No gitlab oauth configured", http.StatusBadRequest)
		}
		_, ok := session.Get(r.Context(), "gitlab-repos").(config.StringSet)
		if ok {
			http.Error(w, "Already authorized", http.StatusBadRequest)
		}

		var expiration = time.Now().Add(24 * time.Hour * 5)
		state := oauth.GetStateCookie()
		http.SetCookie(
			w,
			&http.Cookie{
				Name:    "oauthstate",
				Value:   state,
				Expires: expiration,
			})

		authUrl := oauth.GitlabConfig.AuthCodeURL(state)
		session.Put(r.Context(), "oauth-provider", "gitlab")
		http.Redirect(w, r, authUrl, http.StatusTemporaryRedirect)
	})

	// OAuth2 provider will redirect back to this URL
	m.HandleFunc("/api/v1/oauth/redirect", func(w http.ResponseWriter, r *http.Request) {
		if err := oauth.VerifyState(r); err != nil {
			http.Error(w, "Failed to verify auth cookie", http.StatusInternalServerError)
		}
		provider := session.GetString(r.Context(), "oauth-provider")
		if provider == "gitlab" {
			token, err := oauth.GetToken(r.FormValue("code"), oauth.GitlabConfig)
			// TODO "Membership = true" might lead to confusion combined
			// configuration possibilities. Should be configurable like gitlab-sync
			gitlabOpts := &config.Gitlab{
				Url:         oauth.GitlabHost,
				Key:         token.AccessToken,
				MaxProjects: 0,
				Membership:  true,
			}
			authRepos, err := gitlabOpts.GetProjectURLs()
			if err != nil {
				http.Error(w, "Failed to retrieve projects", http.StatusInternalServerError)
			}
			s := make(config.StringSet)
			for _, url := range authRepos {
				s.Add(url)
			}
			gob.Register(config.StringSet{}) // scs session manager needs this
			session.Put(r.Context(), "gitlab-repos", s)
		} else {
			http.Error(
				w,
				"Redirect action for oauth provider not implemented",
				http.StatusInternalServerError)
		}
		http.Redirect(w, r, oauth.RedirectHost, http.StatusTemporaryRedirect)
	})

	m.HandleFunc("/api/v1/repos", func(w http.ResponseWriter, r *http.Request) {
		accessKey := r.Header.Get("HOUND-ACCESS-KEY")
		authRepos, ok := session.Get(r.Context(), "gitlab-repos").(config.StringSet)
		if !ok {
			authRepos = config.StringSet{}
		}

		res := map[string]*config.Repo{}
		for name, srch := range idx {
			if srch.Repo.CheckAccess(accessKey, authRepos) {
				res[name] = srch.Repo
			}
		}

		writeResp(w, res)
	})

	m.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		var opt index.SearchOptions
		accessKey := r.Header.Get("HOUND-ACCESS-KEY")
		authRepos, ok := session.Get(r.Context(), "gitlab-repos").(config.StringSet)
		if !ok {
			authRepos = config.StringSet{}
		}

		stats := parseAsBool(r.FormValue("stats"))
		repos := parseAsRepoList(r.FormValue("repos"), idx, accessKey, authRepos)
		query := r.FormValue("q")
		opt.Offset, opt.Limit = parseRangeValue(r.FormValue("rng"))
		opt.FileRegexp = r.FormValue("files")
		opt.ExcludeFileRegexp = r.FormValue("excludeFiles")
		opt.IgnoreCase = parseAsBool(r.FormValue("i"))
		opt.LiteralSearch = parseAsBool(r.FormValue("literal"))
		opt.LinesOfContext = parseAsUintValue(
			r.FormValue("ctx"),
			0,
			maxLinesOfContext,
			defaultLinesOfContext)

		var filesOpened int
		var durationMs int

		results, err := searchAll(query, &opt, repos, idx, &filesOpened, &durationMs)
		if err != nil {
			// TODO(knorton): Return ok status because the UI expects it for now.
			writeError(w, err, http.StatusOK)
			return
		}

		var res struct {
			Results map[string]*index.SearchResponse
			Stats   *Stats `json:",omitempty"`
		}

		res.Results = results
		if stats {
			res.Stats = &Stats{
				FilesOpened: filesOpened,
				Duration:    durationMs,
			}
		}

		writeResp(w, &res)
	})

	m.HandleFunc("/api/v1/excludes", func(w http.ResponseWriter, r *http.Request) {
		repo := r.FormValue("repo")
		res := idx[repo].GetExcludedFiles()
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		w.Header().Set("Access-Control-Allow", "*")
		fmt.Fprint(w, res)
	})

	m.HandleFunc("/api/v1/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeError(w,
				errors.New(http.StatusText(http.StatusMethodNotAllowed)),
				http.StatusMethodNotAllowed)
			return
		}
		accessKey := r.Header.Get("HOUND-ACCESS-KEY")
		authRepos, ok := session.Get(r.Context(), "gitlab-repos").(config.StringSet)
		if !ok {
			authRepos = config.StringSet{}
		}

		repos := parseAsRepoList(r.FormValue("repos"), idx, accessKey, authRepos)

		for _, repo := range repos {
			searcher := idx[repo]
			if searcher == nil {
				writeError(w,
					fmt.Errorf("No such repository: %s", repo),
					http.StatusNotFound)
				return
			}

			if !searcher.Update() {
				writeError(w,
					fmt.Errorf("Push updates are not enabled for repository %s", repo),
					http.StatusForbidden)
				return

			}
		}

		writeResp(w, "ok")
	})

	m.HandleFunc("/api/v1/github-webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeError(w,
				errors.New(http.StatusText(http.StatusMethodNotAllowed)),
				http.StatusMethodNotAllowed)
			return
		}

		type Webhook struct {
			Repository struct {
				Name      string
				Full_name string
			}
		}

		var h Webhook

		err := json.NewDecoder(r.Body).Decode(&h)

		if err != nil {
			writeError(w,
				errors.New(http.StatusText(http.StatusBadRequest)),
				http.StatusBadRequest)
			return
		}

		repo := h.Repository.Full_name

		searcher := idx[h.Repository.Full_name]

		if searcher == nil {
			writeError(w,
				fmt.Errorf("No such repository: %s", repo),
				http.StatusNotFound)
			return
		}

		if !searcher.Update() {
			writeError(w,
				fmt.Errorf("Push updates are not enabled for repository %s", repo),
				http.StatusForbidden)
			return
		}

		writeResp(w, "ok")
	})
}
